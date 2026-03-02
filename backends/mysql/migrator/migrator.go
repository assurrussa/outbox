package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"

	backendmigrations "github.com/assurrussa/outbox/backends/mysql/migrations"
	"github.com/assurrussa/outbox/outbox/logger"
)

type Option func(o *Options)

type Options struct {
	command   string
	directory string
	args      []string
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

func WithArgs(args ...string) Option {
	return func(o *Options) {
		o.args = args
	}
}

func Run(ctx context.Context, db *sql.DB, log logger.Logger, opts ...Option) error {
	goose.SetBaseFS(nil)

	options := Options{
		command:   "status",
		directory: "migrations",
		args:      nil,
	}

	for _, o := range opts {
		o(&options)
	}

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	if err := goose.RunWithOptionsContext(ctx, options.command, db, options.directory, options.args); err != nil {
		if errors.Is(err, goose.ErrNoMigrationFiles) {
			message := fmt.Sprintf("migrate DB for command: %s in dir: %s", options.command, options.directory)
			log.InfoContext(ctx, message, slog.String("status", "fail"), logger.Error(err))
			return nil
		}

		return fmt.Errorf("failed to run database migrations: %w", err)
	}

	return nil
}

// RunEmbedded executes migrations bundled into the backend module.
func RunEmbedded(ctx context.Context, db *sql.DB, log logger.Logger, opts ...Option) error {
	goose.SetBaseFS(backendmigrations.FS)
	defer goose.SetBaseFS(nil)

	options := Options{
		command:   "status",
		directory: ".",
		args:      nil,
	}

	for _, o := range opts {
		o(&options)
	}

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	if err := goose.RunWithOptionsContext(ctx, options.command, db, options.directory, options.args); err != nil {
		if errors.Is(err, goose.ErrNoMigrationFiles) {
			message := fmt.Sprintf("migrate DB for command: %s in dir: %s", options.command, options.directory)
			log.InfoContext(ctx, message, slog.String("status", "fail"), logger.Error(err))
			return nil
		}

		return fmt.Errorf("failed to run database migrations: %w", err)
	}

	return nil
}
