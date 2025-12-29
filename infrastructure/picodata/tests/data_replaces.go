//go:build integration

package tests

import (
	"strings"

	"github.com/google/uuid"
)

func TestDataReplaces() []OptionDatabase {
	randomName := strings.ReplaceAll(uuid.New().String(), "-", "")[16:]
	data := map[string]ReplaceTableName{
		"outbox_jobs_failed": {Name: "outbox_jobs_" + randomName + "_failed", Replace: false},
		"outbox_jobs":        {Name: "outbox_jobs_" + randomName, Replace: true},
	}

	fn := func(key string) string {
		val, ok := data[key]
		if !ok {
			return key
		}

		return val.Name
	}

	return []OptionDatabase{
		WithDatabaseTableReplaces(data),
		WithDatabaseFnReplaceTableNameGetter(fn),
	}
}
