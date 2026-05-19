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
