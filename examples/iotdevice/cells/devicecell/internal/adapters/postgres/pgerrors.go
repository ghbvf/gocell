// Package postgres provides cell-private PostgreSQL implementations of the
// devicecell port interfaces. These implementations live inside the cell's
// internal package tree so they can import the cell's own internal/domain
// without violating Go module visibility rules — adapters/ cannot import
// examples/*/internal/..., but the reverse is allowed.
//
// Layering note: this package does NOT import adapters/postgres. Instead, it
// duplicates the minimal SQLSTATE classifier helpers needed for error mapping.
// The SQLSTATE strings are stable PostgreSQL constants (not implementation
// details), so duplication here is acceptable.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// PG SQLSTATE codes used by cell-private repo error classifiers.
// Duplicated from adapters/postgres/errors.go — examples/ cannot import adapters/
// via cells/ layering rules. These are stable PostgreSQL wire-level identifiers.
//
// ref: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// sqlStateUniqueViolation is class 23 / 23505 (unique constraint).
	sqlStateUniqueViolation = "23505"
)

// isUniqueViolation reports whether err is a PG unique-constraint violation
// (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlStateUniqueViolation
}
