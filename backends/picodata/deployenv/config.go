package deployenv

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	envOutboxPicodataDSN      = "OUTBOX_PICODATA_DSN"
	envTestOutboxPicodataDSN  = "TEST_OUTBOXLIB_PICODATA_DSN"
	envOutboxPicodataHost     = "OUTBOX_PICODATA_HOST"
	envOutboxPicodataPort     = "OUTBOX_PICODATA_PORT"
	envOutboxPicodataUser     = "OUTBOX_PICODATA_USER"
	envOutboxPicodataPassword = "OUTBOX_PICODATA_PASSWORD"
	envOutboxPicodataSSLMode  = "OUTBOX_PICODATA_SSLMODE"

	envPicodataListen          = "PICODATA_LISTEN"
	envPicodataIProtoListen    = "PICODATA_IPROTO_LISTEN"
	envPicodataPGAdvertise     = "PICODATA_PG_ADVERTISE"
	envPicodataIProtoAdvertise = "PICODATA_IPROTO_ADVERTISE"
)

const (
	defaultHost     = "127.0.0.1"
	defaultPort     = 5049
	defaultUser     = "admin"
	defaultPassword = "passWord!123"
	defaultSSLMode  = "disable"
)

type AppConnConfig struct {
	DSN      string
	Host     string
	Port     int
	User     string
	Password string
	SSLMode  string
}

func LoadAppConnFromEnv(lookup func(string) string) (AppConnConfig, error) {
	get := makeLookup(lookup)

	if dsn := firstNonEmpty(get(envOutboxPicodataDSN), get(envTestOutboxPicodataDSN)); dsn != "" {
		cfg, err := parseDSN(dsn)
		if err != nil {
			return AppConnConfig{}, fmt.Errorf("load picodata dsn: %w", err)
		}

		return cfg, nil
	}

	host := normalizeHost(firstNonEmpty(get(envOutboxPicodataHost), defaultHost))
	if err := validateClientHost(host); err != nil {
		return AppConnConfig{}, err
	}

	port, err := parsePort(firstNonEmpty(get(envOutboxPicodataPort), strconv.Itoa(defaultPort)))
	if err != nil {
		return AppConnConfig{}, fmt.Errorf("invalid %s: %w", envOutboxPicodataPort, err)
	}

	cfg := AppConnConfig{
		Host:     host,
		Port:     port,
		User:     firstNonEmpty(get(envOutboxPicodataUser), defaultUser),
		Password: firstNonEmpty(get(envOutboxPicodataPassword), defaultPassword),
		SSLMode:  firstNonEmpty(get(envOutboxPicodataSSLMode), defaultSSLMode),
	}
	cfg.DSN = cfg.ConnectionURL()

	return cfg, nil
}

func (c AppConnConfig) ConnectionURL() string {
	if strings.TrimSpace(c.DSN) != "" {
		return strings.TrimSpace(c.DSN)
	}

	host := normalizeHost(firstNonEmpty(c.Host, defaultHost))
	port := c.Port
	if port == 0 {
		port = defaultPort
	}
	user := firstNonEmpty(c.User, defaultUser)
	password := firstNonEmpty(c.Password, defaultPassword)
	sslMode := firstNonEmpty(c.SSLMode, defaultSSLMode)

	dsnURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		RawQuery: "sslmode=" + url.QueryEscape(sslMode),
	}

	return dsnURL.String()
}

func ValidateRuntimeEnv(lookup func(string) string) error {
	get := makeLookup(lookup)

	if get(envPicodataListen) != "" && get(envPicodataIProtoListen) != "" {
		return errors.New(
			"picodata runtime env conflict: PICODATA_LISTEN and PICODATA_IPROTO_LISTEN are mutually exclusive; remove PICODATA_LISTEN and keep PICODATA_IPROTO_LISTEN",
		)
	}

	if get(envPicodataPGAdvertise) != "" && get(envPicodataIProtoAdvertise) != "" {
		return errors.New(
			"picodata runtime env conflict: PICODATA_PG_ADVERTISE and PICODATA_IPROTO_ADVERTISE are mutually exclusive; remove PICODATA_PG_ADVERTISE and keep PICODATA_IPROTO_ADVERTISE",
		)
	}

	return nil
}

func parseDSN(rawDSN string) (AppConnConfig, error) {
	dsnURL, err := url.Parse(strings.TrimSpace(rawDSN))
	if err != nil {
		return AppConnConfig{}, fmt.Errorf("parse dsn: %w", err)
	}

	if dsnURL.Scheme == "" {
		return AppConnConfig{}, errors.New("dsn must include scheme, for example postgres://")
	}

	host := normalizeHost(dsnURL.Hostname())
	if host == "" {
		return AppConnConfig{}, errors.New("dsn host is empty")
	}
	if err := validateClientHost(host); err != nil {
		return AppConnConfig{}, err
	}

	portText := dsnURL.Port()
	if portText == "" {
		portText = strconv.Itoa(defaultPort)
	}
	port, err := parsePort(portText)
	if err != nil {
		return AppConnConfig{}, fmt.Errorf("invalid dsn port: %w", err)
	}

	dsnURL.Host = net.JoinHostPort(host, strconv.Itoa(port))

	query := dsnURL.Query()
	sslMode := strings.TrimSpace(query.Get("sslmode"))
	if sslMode == "" {
		sslMode = defaultSSLMode
		query.Set("sslmode", sslMode)
		dsnURL.RawQuery = query.Encode()
	}

	cfg := AppConnConfig{
		DSN:     dsnURL.String(),
		Host:    host,
		Port:    port,
		SSLMode: sslMode,
	}

	if dsnURL.User != nil {
		cfg.User = strings.TrimSpace(dsnURL.User.Username())
		cfg.Password, _ = dsnURL.User.Password()
	}

	return cfg, nil
}

func parsePort(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	if value <= 0 || value > 65535 {
		return 0, errors.New("port must be between 1 and 65535")
	}

	return value, nil
}

func validateClientHost(host string) error {
	if normalizeHost(host) == "0.0.0.0" {
		return errors.New("picodata client host must not be 0.0.0.0")
	}

	return nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return defaultHost
	}

	return host
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}

	return ""
}

func makeLookup(lookup func(string) string) func(string) string {
	if lookup == nil {
		return func(string) string { return "" }
	}

	return func(key string) string {
		return strings.TrimSpace(lookup(key))
	}
}
