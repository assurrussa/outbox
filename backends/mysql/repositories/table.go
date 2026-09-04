package repositories

import (
	"errors"
	"fmt"
	stdstrings "strings"
)

// ValidateAndQuoteTableName validates and quotes a MySQL table name.
// Supports 1 or 2 parts (e.g. "table" or "db.table"), each up to 64 characters
// matching [A-Za-z_][A-Za-z0-9_]*, and quotes each part with MySQL backticks.
func ValidateAndQuoteTableName(name string) (string, error) {
	trimmed := stdstrings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("mysql: table name is empty")
	}

	parts := stdstrings.Split(trimmed, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("mysql: invalid table name %q: too many schema parts", trimmed)
	}

	quotedParts := make([]string, 0, len(parts))
	for _, part := range parts {
		p := stdstrings.TrimSpace(part)
		p = stdstrings.Trim(p, "`")
		if p == "" {
			return "", fmt.Errorf("mysql: invalid table name %q: empty part", trimmed)
		}
		if len(p) > 64 {
			return "", fmt.Errorf("mysql: table identifier part %q exceeds 64 characters", p)
		}
		for i, r := range p {
			if i == 0 {
				if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && r != '_' {
					return "", fmt.Errorf("mysql: invalid table name %q: must start with a letter or underscore", trimmed)
				}
			} else {
				if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
					return "", fmt.Errorf("mysql: invalid table name %q: contains invalid character %c", trimmed, r)
				}
			}
		}
		quotedParts = append(quotedParts, fmt.Sprintf("`%s`", p))
	}

	return stdstrings.Join(quotedParts, "."), nil
}
