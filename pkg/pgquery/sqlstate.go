package pgquery

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// PG SQLSTATE codes used by repo error classifiers. PG codes are stable
// identifiers and are not language-dependent, so ad-hoc string compares are
// the idiomatic Go convention.
//
// ref: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// SQLStateUniqueViolation is class 23 / 23505 (unique constraint).
	// pgconn.PgError.Code carries this value verbatim.
	SQLStateUniqueViolation = "23505"
	// SQLStateForeignKeyViolation is class 23 / 23503.
	SQLStateForeignKeyViolation = "23503"
	// SQLStateRaiseException is the catch-all class P0001 used by
	// PL/pgSQL `RAISE EXCEPTION`.
	SQLStateRaiseException = "P0001"
)

// IsUniqueViolation reports whether err (or any error in its Unwrap chain)
// is a PG unique-constraint violation (SQLSTATE 23505). Repo callers wrap
// the result as a domain ErrAuth*Duplicate to keep the wire-level errcode
// stable across mem and PG backends.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == SQLStateUniqueViolation
}

// IsForeignKeyViolation reports whether err (or any error in its Unwrap chain)
// is a PG foreign-key violation (SQLSTATE 23503). Used by role_assignments to
// classify a delete that would orphan downstream rows.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == SQLStateForeignKeyViolation
}

// IsRaiseException reports whether err (or any error in its Unwrap chain) is a
// PL/pgSQL RAISE EXCEPTION (SQLSTATE P0001). P0001 is the generic class used by
// any RAISE EXCEPTION site, so a true result only tells the caller the error
// originated from a trigger/function RAISE — callers that need to attribute it
// to a specific trigger must additionally inspect pgconn.PgError.Message
// (e.g. accesscore isLastAdminProtected matches the trigger message prefix).
// Keeping the SQLSTATE classification here keeps pkg/pgquery the single source
// for PG error-code knowledge.
func IsRaiseException(err error) bool {
	pgErr, ok := AsRaiseException(err)
	_ = pgErr
	return ok
}

// AsRaiseException returns the unwrapped *pgconn.PgError if err (or any error
// in its Unwrap chain) is a PL/pgSQL RAISE EXCEPTION (SQLSTATE P0001), so
// callers that need both the SQLSTATE classification AND the trigger Message
// attribution can do so in a single Unwrap walk. ok=false when err is nil, not
// a pgconn.PgError, or carries a different SQLSTATE.
func AsRaiseException(err error) (*pgconn.PgError, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, false
	}
	if pgErr.Code != SQLStateRaiseException {
		return nil, false
	}
	return pgErr, true
}
