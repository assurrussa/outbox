package loadenv

import (
	"context"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/tools"
)

func Load(files ...string) {
	if err := loadEnvironment(files...); err != nil {
		logger.Default().WarnContext(context.Background(), "not found .env file")
	}
}

func loadEnvironment(files ...string) error {
	callerFile := tools.CallerCurrentFile()
	if len(files) > 0 {
		callerFile = files[0]
	}

	// Надо обязательно до загрузки енв переменных получить значение ENV_OVERRIDE
	envOverride := os.Getenv("ENV_OVERRIDE")

	filePath := tools.FindFileDir(".env", callerFile)
	if err := godotenv.Load(filePath); err != nil {
		return fmt.Errorf("error loading .env file: %w", err)
	}

	if envOverride == "1" {
		filePath := tools.FindFileDir(".env.override", callerFile)
		if err := godotenv.Overload(filePath); err != nil {
			return fmt.Errorf("error loading .env.override file: %w", err)
		}
	}

	return nil
}
