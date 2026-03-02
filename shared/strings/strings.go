package strings //nolint:revive // it's valid name

import "fmt"

func SelectFirst(defaultValue string, values ...string) string {
	for _, name := range values {
		return name
	}

	return defaultValue
}

func Concate(query string, tableName string) string {
	return fmt.Sprintf(query, tableName)
}
