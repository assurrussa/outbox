package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
)

var (
	ErrRowAlreadyExists = errors.New("row already exists")
	ErrNoRows           = errors.New("no rows found")
)

func ErrorTransform(err error) error {
	if err == nil {
		return nil
	}

	if pgxscan.NotFound(err) {
		return fmt.Errorf("postgres error: %w", errors.Join(ErrNoRows, err))
	}

	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("postgres error: %w", errors.Join(ErrNoRows, err))
	}

	if strings.Contains(err.Error(), "23505") {
		return fmt.Errorf("postgres error: %w", errors.Join(ErrRowAlreadyExists, err))
	}

	return err
}
