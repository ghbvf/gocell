package postgres

import (
	"github.com/jackc/pgx/v5"
)

// RowScanner abstracts a single-row scan target, satisfied by both pgx.Row
// and *pgx.Rows. Repository implementations use this to write scan logic
// that works for both QueryRow and Query result sets.
type RowScanner interface {
	// Scan reads column values into dest, analogous to pgx.Row.Scan.
	Scan(dest ...any) error
}

// Ensure pgx.Row satisfies RowScanner at compile time.
// pgx.Rows also satisfies RowScanner (via its Scan method) but is itself an
// interface, so a compile-time assertion is omitted.
var _ RowScanner = (pgx.Row)(nil)
