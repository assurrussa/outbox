package migrations

import "embed"

// FS contains bundled SQL migrations for SQLite backend.
//
//go:embed *.sql
var FS embed.FS
