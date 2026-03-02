package migrations

import "embed"

// FS contains bundled SQL migrations for Picodata backend.
//
//go:embed *.sql
var FS embed.FS
