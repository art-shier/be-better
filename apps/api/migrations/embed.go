package migrations

import "embed"

// FS contains the forward-only PostgreSQL schema migrations used by the
// standalone migrate command and integration tests.
//
//go:embed *.up.sql
var FS embed.FS
