package transaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Manager struct {
	db *sql.DB
}

func New(db *sql.DB) *Manager {
	return &Manager{db: db}
}

func (m *Manager) RunInTx(ctx context.Context, fn func(context.Context) error) (err error) {
	if m == nil || m.db == nil {
		return errors.New("sqlite transaction manager is not configured")
	}

	if tx := GetTx(ctx); tx != nil {
		return fn(ctx)
	}

	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	ctx = WithTx(ctx, tx)

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v", r)
		}

		if err == nil {
			err = tx.Commit()
			if err != nil {
				err = fmt.Errorf("commit: %w", err)
			}
			return
		}

		if rbErr := tx.Rollback(); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
	}()

	return fn(ctx)
}
