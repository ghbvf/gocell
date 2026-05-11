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
	// PL/pgSQL RAISE EXCEPTION (e.g. effective_admin_invariant_fn trigger).
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
// by the effective_admin_invariant_fn trigger function
// (migrations/024_effective_admin_invariant.sql). Distinct from the bare
// SQLSTATE check because P0001 is a generic class — we also need the trigger
// sentinel in the MESSAGE field to avoid catching unrelated RAISE EXCEPTION
// sites.
//
// S4.0 (migration 024) renamed the trigger function from
// `last_admin_protected_fn` → `effective_admin_invariant_fn` and changed the
// message prefix accordingly. The 019 trigger / function are fully retired.
func isLastAdminProtected(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != sqlStateRaiseException {
		return false
	}
	// Match prefix only — the trigger function emits
	// 'effective_admin_invariant: would leave the system with no effective admin'
	// (see migrations/024_effective_admin_invariant.sql).
	const triggerSentinel = "effective_admin_invariant"
	for i := 0; i+len(triggerSentinel) <= len(pgErr.Message); i++ {
		if pgErr.Message[i:i+len(triggerSentinel)] == triggerSentinel {
			return true
		}
	}
	return false
}
