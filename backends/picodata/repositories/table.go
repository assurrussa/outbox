package repositories

import (
	"errors"
	"fmt"
	stdstrings "strings"
)

// ValidateAndQuoteTableName validates and quotes a Picodata table name.
// Table name must be a single identifier up to 64 characters matching
// [A-Za-z_][A-Za-z0-9_]*, and is quoted with double quotes.
func ValidateAndQuoteTableName(name string) (string, error) {
	trimmed := stdstrings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("picodata: table name is empty")
	}

	p := stdstrings.Trim(trimmed, "\"")
	if p == "" {
		return "", fmt.Errorf("picodata: invalid table name %q: empty identifier", trimmed)
	}
	if len(p) > 64 {
		return "", fmt.Errorf("picodata: table identifier %q exceeds 64 characters", p)
	}
	for i, r := range p {
		if i == 0 {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && r != '_' {
				return "", fmt.Errorf("picodata: invalid table name %q: must start with a letter or underscore", trimmed)
			}
		} else {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
				return "", fmt.Errorf("picodata: invalid table name %q: contains invalid character %c", trimmed, r)
			}
		}
	}

	return fmt.Sprintf("\"%s\"", p), nil
}
