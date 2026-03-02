package prettier

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/assurrussa/outbox/outbox/logger"
)

const (
	PlaceholderDollar = "$"
)

// LogQuery - Внимание! используем только в локалном окружении!!!!
func LogQuery(ctx context.Context, log logger.Logger, env string, sql string, operationName string, args []any) {
	if log == nil {
		return
	}
	if env != "local" && env != "dev" {
		return
	}

	prettyQuery := pretty(sql, PlaceholderDollar, args...)

	log.DebugContext(
		ctx,
		"sql query",
		slog.String("sql", operationName),
		slog.String("query", prettyQuery),
	)
}

func pretty(sql string, placeholder string, args ...any) string {
	for i, param := range args {
		var value string
		switch v := param.(type) {
		case string:
			value = fmt.Sprintf("%q", v)
		case []byte:
			value = fmt.Sprintf("%q", string(v))
		default:
			value = fmt.Sprintf("%v", v)
		}

		sql = strings.ReplaceAll(sql, fmt.Sprintf("%s%s", placeholder, strconv.Itoa(i+1)), value)
	}

	sql = strings.ReplaceAll(sql, "\t", "")
	sql = strings.ReplaceAll(sql, "\n", " ")

	return strings.TrimSpace(sql)
}
