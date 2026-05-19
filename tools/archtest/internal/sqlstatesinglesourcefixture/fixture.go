//go:build archtest_fixture

// Package sqlstatesinglesourcefixture contains intentionally-violating and
// intentionally-clean SQLSTATE classification snippets that exercise the
// SQLSTATE-SINGLE-SOURCE-01 archtest.
//
// Gated by the archtest_fixture build tag; production builds never see this
// package. Loaded by TestSQLStateSingleSource_RedFixtureDetected via
// archtest.RunTypedFixture.
//
// # RED cases (must each produce exactly one diagnostic)
//
//   - badUnique:    pgErr.Code == "23505"  (BinaryExpr ==, owned literal)
//   - badNotRaise:  pgErr.Code != "P0001"  (BinaryExpr !=, owned literal)
//   - badFKSwitch:  switch pgErr.Code { case "23503": } (SwitchStmt, owned literal)
//
// # GREEN controls (must produce zero diagnostics)
//
//   - okTransient:  pgErr.Code == "40001"  (non-owned SQLSTATE — transient
//     classifier domain, legitimately single-sourced elsewhere)
//   - okConstraint: pgErr.ConstraintName == "x" (different field, not .Code)
//   - okErrorsAs:   errors.As only, no .Code read
//
// Total expected diagnostics: 3.
package sqlstatesinglesourcefixture

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// badUnique RED: reads pgconn.PgError.Code and compares to the pkg/pgquery-owned
// unique-violation SQLSTATE literal — a duplicated classifier.
func badUnique(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" // RED: owned SQLSTATE compared outside pkg/pgquery
}

// badNotRaise RED: != form against the owned RAISE EXCEPTION literal.
func badNotRaise(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "P0001" { // RED: owned SQLSTATE compared outside pkg/pgquery
		return false
	}
	return true
}

// badFKSwitch RED: switch on pgErr.Code with an owned literal case clause.
func badFKSwitch(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	switch pgErr.Code {
	case "23503": // RED: owned SQLSTATE in switch case outside pkg/pgquery
		return "fk"
	}
	return ""
}

// okTransient GREEN: a non-owned SQLSTATE (serialization_failure) — this is the
// transient-classifier domain, single-sourced in adapters/postgres/classify.go,
// NOT a duplicate of the pkg/pgquery unique/FK/raise classifiers.
func okTransient(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" // GREEN: not in the owned set
}

// okConstraint GREEN: reads a different PgError field, not .Code.
func okConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	if pgErr.ConstraintName == "role_assignments_user_id_fkey" { // GREEN: not .Code
		return "user"
	}
	return ""
}

// okErrorsAs GREEN: extracts the PgError but never inspects .Code.
func okErrorsAs(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr)
}
