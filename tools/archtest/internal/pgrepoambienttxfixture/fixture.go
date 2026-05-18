//go:build archtest_fixture

// Package pgrepoambienttxfixture contains intentionally-violating struct
// definitions and function signatures that exercise the PG-REPO-AMBIENT-TX-01
// struct-field-funnel archtest.
//
// Gated by the archtest_fixture build tag; production builds never see this
// package. The fixture is loaded by TestPGRepoAmbientTx_RedFixtureDetected via
// archtest.RunTypedFixture (which injects the archtest_fixture tag inside its
// body).
//
// # Violations in this fixture
//
// R1 — struct-field funnel: only pgExecutor may hold a *pgxpool.Pool field.
//
//   - badR1Repo: a struct that is NOT named pgExecutor but holds *pgxpool.Pool.
//     Must produce exactly one R1 diagnostic.
//
// R2 — constructor-param funnel: *pgxpool.Pool params may only appear in
// New*-prefixed funcs that call newPGExecutor with that param.
//
//   - badR2NonNew: a non-New* function that takes *pgxpool.Pool.
//     Must produce exactly one R2 diagnostic.
//
//   - NewBadR2NoWrap: a New*-prefixed function that takes *pgxpool.Pool but
//     does NOT call newPGExecutor. Must produce exactly one R2 diagnostic.
//
// GREEN control: NewGoodRepo takes *pgxpool.Pool and calls newPGExecutor,
// and pgExecutor is the single struct holding the pool field. Zero diagnostics.
//
// Total expected diagnostics: 3 (one R1 + two R2).
package pgrepoambienttxfixture

import "github.com/jackc/pgx/v5/pgxpool"

// pgExecutor is the ONE sanctioned struct allowed to hold *pgxpool.Pool.
// Its presence here is the GREEN control — R1 must NOT flag it.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// newPGExecutor is the sanctioned constructor. Referenced by goodNewFoo below.
func newPGExecutor(pool *pgxpool.Pool) pgExecutor {
	return pgExecutor{pool: pool}
}

// badR1Repo is a struct that holds *pgxpool.Pool but is NOT named pgExecutor.
// R1 must flag this as a violation.
type badR1Repo struct {
	pool *pgxpool.Pool // R1 violation: only pgExecutor may hold *pgxpool.Pool
}

// badR2NonNew is a non-New* function that takes *pgxpool.Pool.
// R2 must flag this: *pgxpool.Pool params may only appear on New* constructors.
func badR2NonNew(pool *pgxpool.Pool) *badR1Repo {
	return &badR1Repo{pool: pool}
}

// NewBadR2NoWrap is a New*-prefixed function that accepts *pgxpool.Pool but
// does NOT call newPGExecutor. R2 must flag this.
func NewBadR2NoWrap(pool *pgxpool.Pool) *badR1Repo {
	return &badR1Repo{pool: pool}
}

// NewGoodRepo is a New*-prefixed function that accepts *pgxpool.Pool AND calls
// newPGExecutor — the correct constructor pattern. R2 must NOT flag this.
func NewGoodRepo(pool *pgxpool.Pool) pgExecutor {
	return newPGExecutor(pool)
}
