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
// Callers must handle the returned error before passing the filesystem to
// NewMigrator.
func MigrationsFS() (fs.FS, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	return sub, nil
}
