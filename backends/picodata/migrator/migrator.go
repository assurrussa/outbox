package migrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/assurrussa/outbox/backends/picodata"
	backendmigrations "github.com/assurrussa/outbox/backends/picodata/migrations"
	"github.com/assurrussa/outbox/outbox/logger"
)

const (
	upMarker                   = "-- pico.UP"
	downMarker                 = "-- pico.DOWN"
	defaultMigrationsTableName = "picodata_db_version"
)

type Option func(o *Options)

type Options struct {
	command                   string
	directory                 string
	tableName                 string
	steps                     int
	args                      []string
	databaseTableReplacesList map[string]string
}

func WithCommand(command string) Option {
	return func(o *Options) {
		o.command = command
	}
}

func WithDirectory(directory string) Option {
	return func(o *Options) {
		o.directory = directory
	}
}

func WithTableName(tableName string) Option {
	return func(o *Options) {
		o.tableName = tableName
	}
}

func WithArgs(args ...string) Option {
	return func(o *Options) {
		o.args = args
	}
}

func WithSteps(steps int) Option {
	return func(o *Options) {
		o.steps = steps
	}
}

func WithDatabaseTableReplacesList(databaseTableReplacesList map[string]string) Option {
	return func(o *Options) {
		o.databaseTableReplacesList = databaseTableReplacesList
	}
}

type migrationFile struct {
	name    string
	version int64
	up      string
	down    string
}

type appliedMigration struct {
	name      string
	applied   bool
	appliedAt time.Time
}

func Run(ctx context.Context, picoPool picodata.Client, log logger.Logger, opts ...Option) (errReturn error) {
	options := Options{
		command:                   "status",
		directory:                 "migrations",
		tableName:                 "public",
		args:                      nil,
		databaseTableReplacesList: nil,
		steps:                     1,
	}

	for _, o := range opts {
		o(&options)
	}

	migrations, err := readMigrations(options.directory, options.databaseTableReplacesList)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	sanitizedTableName, err := sanitizeTableName(options.tableName)
	if err != nil {
		return fmt.Errorf("table name: %w", err)
	}

	if err := ensureMigrationsTable(ctx, picoPool, sanitizedTableName); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	appliedMigrations, err := loadAppliedMigrations(ctx, picoPool, sanitizedTableName)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}

	log.InfoContext(ctx, "loaded migrations",
		slog.Int("count", len(migrations)),
		slog.Int("steps", options.steps),
		slog.String("dir", options.directory),
	)

	switch strings.ToLower(options.command) {
	case "status":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		return printMigrationsStatus(ctx, migrations, appliedMigrations, maxAppliedVersion, log)
	case "up":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		return applyMigrations(ctx, picoPool, migrations, sanitizedTableName, appliedMigrations, maxAppliedVersion, log)
	case "down":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		return rollbackMigrations(
			ctx, picoPool, migrations, sanitizedTableName, appliedMigrations, maxAppliedVersion, options.steps, log,
		)
	case "reset":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		resetSteps := countAppliedWithAssumptions(migrations, appliedMigrations, maxAppliedVersion)
		return rollbackMigrations(
			ctx, picoPool, migrations, sanitizedTableName, appliedMigrations, maxAppliedVersion, resetSteps, log,
		)
	default:
		return fmt.Errorf("unsupported command %q", options.command)
	}
}

// RunEmbedded executes migrations bundled into the backend module.
func RunEmbedded(ctx context.Context, picoPool picodata.Client, log logger.Logger, opts ...Option) error {
	options := Options{
		command:                   "status",
		directory:                 ".",
		tableName:                 "public",
		args:                      nil,
		databaseTableReplacesList: nil,
		steps:                     1,
	}

	for _, o := range opts {
		o(&options)
	}

	migrations, err := readMigrationsFromFS(backendmigrations.FS, options.directory, options.databaseTableReplacesList)
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	sanitizedTableName, err := sanitizeTableName(options.tableName)
	if err != nil {
		return fmt.Errorf("table name: %w", err)
	}

	if err := ensureMigrationsTable(ctx, picoPool, sanitizedTableName); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	appliedMigrations, err := loadAppliedMigrations(ctx, picoPool, sanitizedTableName)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}

	log.InfoContext(ctx, "loaded embedded migrations",
		slog.Int("count", len(migrations)),
		slog.Int("steps", options.steps),
		slog.String("dir", options.directory),
	)

	switch strings.ToLower(options.command) {
	case "status":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		return printMigrationsStatus(ctx, migrations, appliedMigrations, maxAppliedVersion, log)
	case "up":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		return applyMigrations(ctx, picoPool, migrations, sanitizedTableName, appliedMigrations, maxAppliedVersion, log)
	case "down":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		return rollbackMigrations(
			ctx, picoPool, migrations, sanitizedTableName, appliedMigrations, maxAppliedVersion, options.steps, log,
		)
	case "reset":
		maxAppliedVersion := findMaxAppliedVersion(appliedMigrations)
		resetSteps := countAppliedWithAssumptions(migrations, appliedMigrations, maxAppliedVersion)
		return rollbackMigrations(
			ctx, picoPool, migrations, sanitizedTableName, appliedMigrations, maxAppliedVersion, resetSteps, log,
		)
	default:
		return fmt.Errorf("unsupported command %q", options.command)
	}
}

func sanitizeTableName(tableName string) (string, error) {
	name := strings.TrimSpace(tableName)
	if name == "" {
		name = defaultMigrationsTableName
	}

	for i, r := range name {
		switch {
		case r == '_':
			continue
		case r >= 'a' && r <= 'z':
			continue
		case r >= 'A' && r <= 'Z':
			continue
		case r >= '0' && r <= '9' && i > 0:
			continue
		default:
			return "", fmt.Errorf("invalid table name %q", tableName)
		}
	}

	return name, nil
}

func ensureMigrationsTable(ctx context.Context, picoPool picodata.Client, tableName string) error {
	query := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    version_id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at DATETIME NOT NULL,
    applied INTEGER NOT NULL
) USING memtx DISTRIBUTED BY (version_id)
OPTION (TIMEOUT = 3.0);`, tableName)

	if _, err := picoPool.Pool().Exec(ctx, query); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	return nil
}

func loadAppliedMigrations(
	ctx context.Context,
	picoPool picodata.Client,
	tableName string,
) (map[int64]appliedMigration, error) {
	query := fmt.Sprintf(`select version_id, name, applied, applied_at from %s;`, tableName)

	rows, err := picoPool.Pool().Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]appliedMigration)
	for rows.Next() {
		var (
			version   int64
			name      string
			appliedDB int64
			appliedAt time.Time
		)

		if errScan := rows.Scan(&version, &name, &appliedDB, &appliedAt); errScan != nil {
			return nil, fmt.Errorf("scan applied migrations: %w", errScan)
		}

		applied[version] = appliedMigration{
			name:      name,
			applied:   appliedDB == 1,
			appliedAt: appliedAt,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}

	return applied, nil
}

func printMigrationsStatus(
	ctx context.Context,
	migrations []migrationFile,
	applied map[int64]appliedMigration,
	maxAppliedVersion int64,
	log logger.Logger,
) error {
	knownVersions := make(map[int64]struct{}, len(migrations))
	for _, m := range migrations {
		knownVersions[m.version] = struct{}{}

		state := "pending"
		appliedAt := time.Time{}
		if appliedMigration, ok := applied[m.version]; ok {
			appliedAt = appliedMigration.appliedAt
			if appliedMigration.applied {
				state = "applied"
			} else {
				state = "rolled_back"
			}
		} else if m.version <= maxAppliedVersion {
			state = "assumed_applied"
		}

		fields := []any{
			slog.String("name", m.name),
			slog.Int64("version", m.version),
			slog.String("state", state),
			slog.Bool("has_down", strings.TrimSpace(m.down) != ""),
		}

		if !appliedAt.IsZero() {
			fields = append(fields, slog.Time("applied_at", appliedAt))
		}

		log.InfoContext(ctx, "migration status", fields...)
	}

	for version, appliedMigration := range applied {
		if _, ok := knownVersions[version]; ok {
			continue
		}

		log.WarnContext(ctx, "migration exists in database but missing from filesystem",
			slog.Int64("version", version),
			slog.String("name", appliedMigration.name),
			slog.Bool("applied", appliedMigration.applied),
		)
	}

	return nil
}

func recordMigrationState(
	ctx context.Context,
	picoPool picodata.Client,
	tableName string,
	migration migrationFile,
	applied bool,
) (time.Time, error) {
	appliedAt := time.Now().UTC()

	appliedFlag := 0
	if applied {
		appliedFlag = 1
	}

	if !applied {
		deleteQuery := fmt.Sprintf(`delete from %s where version_id=$1;`, tableName)
		if _, err := picoPool.Pool().Exec(ctx, deleteQuery, migration.version); err != nil {
			return time.Time{}, fmt.Errorf("delete migration state for %s: %w", migration.name, err)
		}
		return appliedAt, nil
	}

	insertQuery := fmt.Sprintf(`
INSERT INTO %s (version_id, name, applied_at, applied) VALUES ($1, $2, $3, $4);`, tableName)

	if _, err := picoPool.Pool().Exec(ctx, insertQuery, migration.version, migration.name, appliedAt, appliedFlag); err != nil {
		return time.Time{}, fmt.Errorf("store migration state for %s: %w", migration.name, err)
	}

	return appliedAt, nil
}

func findMaxAppliedVersion(applied map[int64]appliedMigration) int64 {
	var maxVersion int64 = -1
	for version, migration := range applied {
		if migration.applied && version > maxVersion {
			maxVersion = version
		}
	}

	return maxVersion
}

func countAppliedWithAssumptions(
	migrations []migrationFile,
	applied map[int64]appliedMigration,
	maxAppliedVersion int64,
) int {
	count := 0
	for _, migration := range migrations {
		if appliedState, ok := applied[migration.version]; ok {
			if appliedState.applied {
				count++
			}
			continue
		}

		if maxAppliedVersion >= 0 && migration.version <= maxAppliedVersion {
			count++
		}
	}

	return count
}

func alreadyAppliedByName(applied map[int64]appliedMigration, name string) bool {
	for _, state := range applied {
		if state.applied && state.name == name {
			return true
		}
	}

	return false
}

// readMigrations Есть ряд особенностей, пока они не решены.
// 1) Таблицы должны называтся для замены по разному и не иметь общих начал
// 2) Если таблицы все таки имеют общие начала, то есть смысл обьединить их вгруппу для замены сразу.
// 3) Или придумать механизм, как доставать потом данные которые либо были заменены либо нет, где-то из вне.
func readMigrations(directory string, databaseTableReplaces map[string]string) ([]migrationFile, error) {
	dir := filepath.Clean(directory)

	return readMigrationsFromFS(os.DirFS(dir), ".", databaseTableReplaces)
}

func readMigrationsFromFS(
	fsys fs.FS,
	directory string,
	databaseTableReplaces map[string]string,
) ([]migrationFile, error) {
	dir := strings.TrimSpace(directory)
	if dir == "" {
		dir = "."
	}

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	migrations := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, err := parseMigrationVersion(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("parse migration name %s: %w", entry.Name(), err)
		}

		filePath := path.Join(dir, entry.Name())
		content, err := fs.ReadFile(fsys, filePath)
		if err != nil {
			return nil, fmt.Errorf("read file %s: %w", filePath, err)
		}

		newContent := string(content)
		for currTable, newTable := range databaseTableReplaces {
			newContent = strings.ReplaceAll(newContent, currTable, newTable)
		}

		mig, err := parseMigration(entry.Name(), version, newContent)
		if err != nil {
			return nil, fmt.Errorf("parse migration %s: %w", filePath, err)
		}

		migrations = append(migrations, mig)
	}

	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].version == migrations[j].version {
			return migrations[i].name < migrations[j].name
		}

		return migrations[i].version < migrations[j].version
	})

	return migrations, nil
}

func parseMigrationVersion(name string) (int64, error) {
	filename := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.SplitN(filename, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("invalid migration file name: %s", name)
	}

	version, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse version from %s: %w", name, err)
	}

	return version, nil
}

func parseMigration(name string, version int64, content string) (migrationFile, error) {
	upIdx := strings.Index(content, upMarker)
	if upIdx == -1 {
		return migrationFile{}, errors.New("missing '-- pico.UP' marker")
	}

	downIdx := strings.Index(content, downMarker)

	var upSQL, downSQL string
	if downIdx == -1 {
		upSQL = strings.TrimSpace(content[upIdx+len(upMarker):])
	} else {
		upSQL = strings.TrimSpace(content[upIdx+len(upMarker) : downIdx])
		downSQL = strings.TrimSpace(content[downIdx+len(downMarker):])
	}

	if upSQL == "" {
		return migrationFile{}, errors.New("empty up migration section")
	}

	return migrationFile{
		name:    name,
		version: version,
		up:      upSQL,
		down:    downSQL,
	}, nil
}

func applyMigrations(
	ctx context.Context,
	picoPool picodata.Client,
	migrations []migrationFile,
	tableName string,
	applied map[int64]appliedMigration,
	maxAppliedVersion int64,
	log logger.Logger,
) error {
	for _, migration := range migrations {
		if appliedState, ok := applied[migration.version]; ok && appliedState.applied {
			log.InfoContext(ctx, "skip already applied migration",
				slog.String("name", migration.name),
				slog.Int64("version", migration.version),
			)
			continue
		}

		if alreadyAppliedByName(applied, migration.name) {
			log.InfoContext(ctx, "skip already applied migration by name",
				slog.String("name", migration.name),
				slog.Int64("version", migration.version),
			)
			continue
		}

		if maxAppliedVersion >= 0 && migration.version <= maxAppliedVersion {
			log.InfoContext(ctx, "skip assumed applied migration",
				slog.String("name", migration.name),
				slog.Int64("version", migration.version),
			)
			continue
		}

		statements, err := splitStatements(migration.up)
		if err != nil {
			return fmt.Errorf("split statements for %s: %w", migration.name, err)
		}

		if len(statements) == 0 {
			continue
		}

		log.InfoContext(ctx, "applying migration",
			slog.String("name", migration.name),
			slog.Int64("version", migration.version),
		)

		for _, stmt := range statements {
			if _, errExec := picoPool.Pool().Exec(ctx, stmt); errExec != nil {
				return fmt.Errorf("exec statement in %s: %w", migration.name, errExec)
			}
		}

		appliedAt, err := recordMigrationState(ctx, picoPool, tableName, migration, true)
		if err != nil {
			return err
		}

		applied[migration.version] = appliedMigration{
			name:      migration.name,
			applied:   true,
			appliedAt: appliedAt,
		}

		log.InfoContext(ctx, "migration applied",
			slog.String("name", migration.name),
			slog.Int64("version", migration.version),
		)
	}

	return nil
}

func rollbackMigrations(
	ctx context.Context,
	picoPool picodata.Client,
	migrations []migrationFile,
	tableName string,
	applied map[int64]appliedMigration,
	maxAppliedVersion int64,
	steps int,
	log logger.Logger,
) error {
	if len(migrations) == 0 || steps <= 0 {
		return nil
	}

	for i := len(migrations) - 1; i >= 0 && steps > 0; i-- {
		migration := migrations[i]
		appliedState, ok := applied[migration.version]
		shouldRollback := (ok && appliedState.applied) || (!ok && maxAppliedVersion >= 0 && migration.version <= maxAppliedVersion)
		if !shouldRollback {
			continue
		}

		if strings.TrimSpace(migration.down) == "" {
			log.WarnContext(ctx, "skip rollback without DOWN section", slog.String("name", migration.name))
			continue
		}

		statements, err := splitStatements(migration.down)
		if err != nil {
			return fmt.Errorf("split down statements for %s: %w", migration.name, err)
		}

		if len(statements) == 0 {
			continue
		}

		log.InfoContext(ctx, "rolling back migration",
			slog.String("name", migration.name),
			slog.Int64("version", migration.version),
		)

		for _, stmt := range statements {
			if _, errExec := picoPool.Pool().Exec(ctx, stmt); errExec != nil {
				return fmt.Errorf("exec rollback statement in %s: %w", migration.name, errExec)
			}
		}

		if _, err := recordMigrationState(ctx, picoPool, tableName, migration, false); err != nil {
			return err
		}

		delete(applied, migration.version)

		log.InfoContext(ctx, "migration rolled back",
			slog.String("name", migration.name),
			slog.Int64("version", migration.version),
		)
		steps--
	}

	return nil
}

func splitStatements(sql string) ([]string, error) {
	var (
		statements []string
		builder    strings.Builder
	)

	scanner := bufio.NewScanner(strings.NewReader(sql))
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = builder.WriteString(line)
		_ = builder.WriteByte('\n')

		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			stmt := strings.TrimSpace(builder.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			builder.Reset()
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if tail := strings.TrimSpace(builder.String()); tail != "" {
		statements = append(statements, tail)
	}

	return statements, nil
}
