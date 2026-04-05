package postgres

import (
	"embed"
	"io/fs"
)

// migrationsFS holds the raw embedded SQL migration files.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrationsFS returns an fs.FS rooted at the migrations/ directory so that
// file entries are directly accessible (e.g. "001_create_outbox_entries.up.sql"
// rather than "migrations/001_create_outbox_entries.up.sql").
// Callers can pass the result directly to NewMigrator.
func MigrationsFS() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		// This can only fail if the embedded path is wrong, which is a build-
		// time guarantee. Panic is acceptable here.
		panic("postgres: embedded migrations sub-directory not found: " + err.Error())
	}
	return sub
}
