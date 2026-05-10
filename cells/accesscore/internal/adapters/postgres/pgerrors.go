// Package postgres provides cell-private PostgreSQL implementations of the
// accesscore port interfaces. These implementations live inside the cell's
// internal package tree so they can import the cell's own internal/ports and
// internal/domain without violating Go module visibility rules — adapters/
// cannot import cells/*/internal/..., but the reverse is allowed.
//
// Layering note: this package does NOT import adapters/postgres. Instead, it
// duplicates the minimal SQLSTATE classifier helpers needed for error mapping.
// The SQLSTATE strings are stable PostgreSQL constants (not implementation
// details), so duplication here is acceptable. See txctx.go comment for the
// kernel/persistence.TxCtxKey sharing contract.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// PG SQLSTATE codes used by cell-private repo error classifiers.
// Duplicated from adapters/postgres/errors.go — cells/ cannot import adapters/.
// These are stable PostgreSQL wire-level identifiers.
//
// ref: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// sqlStateUniqueViolation is class 23 / 23505 (unique constraint).
	sqlStateUniqueViolation = "23505"
	// sqlStateForeignKeyViolation is class 23 / 23503.
	sqlStateForeignKeyViolation = "23503"
	// sqlStateRaiseException is the catch-all class P0001 used by
	// PL/pgSQL RAISE EXCEPTION (e.g. last_admin_protected trigger).
	sqlStateRaiseException = "P0001"
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

// isForeignKeyViolation reports whether err is a PG foreign-key violation
// (SQLSTATE 23503).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlStateForeignKeyViolation
}

// isLastAdminProtected reports whether err is the PL/pgSQL exception raised
// by the last_admin_protected trigger (migrations/019_roles.sql).
func isLastAdminProtected(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != sqlStateRaiseException {
		return false
	}
	const triggerSentinel = "last_admin_protected"
	for i := 0; i+len(triggerSentinel) <= len(pgErr.Message); i++ {
		if pgErr.Message[i:i+len(triggerSentinel)] == triggerSentinel {
			return true
		}
	}
	return false
}
