package migrations

import "embed"

// FS contains bundled SQL migrations for MySQL backend.
//
//go:embed *.sql
var FS embed.FS
