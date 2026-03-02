package migrations

import "embed"

// FS contains bundled SQL migrations for Postgres backend.
//
//go:embed *.sql
var FS embed.FS
