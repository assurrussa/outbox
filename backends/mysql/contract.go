package mysql

import "database/sql"

type Client interface {
	DB() *sql.DB
	Close() error
}
