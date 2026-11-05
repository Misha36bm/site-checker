// Package migrations embeds the SQL migrations of each database engine, so the
// binary carries its own schema.
package migrations

import "embed"

// Postgres holds the PostgreSQL migrations under the "postgres" directory.
//
//go:embed postgres/*.sql
var Postgres embed.FS
