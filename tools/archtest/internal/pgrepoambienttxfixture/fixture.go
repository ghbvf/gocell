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
// R3 — usage-point funnel: same-package repo/store methods must not access
// pgExecutor's unexported pool field directly nor call pgExecutor.ExecDirect
// without a sibling pgrepoapproved.ApprovedExecDirect(<kebab-case-literal>)
// marker in the SAME approval scope (FuncDecl/FuncLit body, not nested closure).
//
//   - badR3PoolDirect: a repo method that accesses r.db.pool directly.
//     Must produce exactly one R3 diagnostic.
//
//   - badR3ExecDirect: a repo method that calls r.db.ExecDirect without
//     any marker. Must produce exactly one R3 diagnostic.
//
// F1 scope-bounded marker checks (PR #917 round-2):
//
//   - badR3MarkerInNestedClosure: marker in nested FuncLit cannot approve
//     outer-scope ExecDirect. One R3 diagnostic at the outer call.
//
//   - badR3MarkerOuterExecInNestedClosure: outer-scope marker cannot approve
//     ExecDirect inside a nested FuncLit. One R3 diagnostic at the inner call.
//
// F2 reason-form-uniqueness checks (PR #917 round-2):
//
//   - badR3ApprovedConstIdent: marker reason is a const identifier (rejected,
//     must be *ast.BasicLit). One R3 diagnostic.
//   - badR3ApprovedConcat: marker reason is "a" + "b" BinaryExpr (rejected).
//     One R3 diagnostic.
//   - badR3ApprovedEmpty: marker reason is "" (fails kebab regex). One R3.
//   - badR3ApprovedPlaceholder: marker reason is "todo" (placeholder rejected).
//     One R3 diagnostic.
//
// GREEN controls: NewGoodRepo takes *pgxpool.Pool and calls newPGExecutor,
// goodExecMethod uses r.db.Exec (sanctioned), pgExecutor holds pool field.
// goodApprovedSingleExecDirect holds a marker + 1 ExecDirect call (R3(b) GREEN).
// goodApprovedMultiExecDirect holds 1 marker + 2 ExecDirect calls (R3(b) GREEN:
// one marker covers all ExecDirect calls in the same scope).
// Zero R1/R2/R3 diagnostics from all GREEN cases.
//
// Total expected diagnostics: 11 (one R1 + two R2 + eight R3 — two for
// pool-direct/ExecDirect-bare + two for F1 nested-closure + four for F2
// reason form).
package pgrepoambienttxfixture

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// pgExecutor is the ONE sanctioned struct allowed to hold *pgxpool.Pool.
// Its presence here is the GREEN control — R1 must NOT flag it.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// newPGExecutor is the sanctioned constructor. Referenced by goodNewFoo below.
func newPGExecutor(pool *pgxpool.Pool) pgExecutor {
	return pgExecutor{pool: pool}
}

// Exec is the sanctioned ambient-tx routing path.
func (e pgExecutor) Exec(ctx context.Context, sql string, args ...any) {
	_, _ = e.pool.Exec(ctx, sql, args...)
}

// ExecDirect is the explicit bypass path reserved for compensation/security
// contexts. Only allowlisted callsites may call this.
func (e pgExecutor) ExecDirect(ctx context.Context, sql string, args ...any) {
	_, _ = e.pool.Exec(ctx, sql, args...)
}

// goodRepo demonstrates the GREEN path: holds a pgExecutor (not pool directly)
// and uses e.db.Exec which routes through the ambient-tx logic.
type goodRepo struct {
	db pgExecutor
}

// goodExecMethod is a GREEN control: uses r.db.Exec (sanctioned routing).
// R3 must NOT flag this.
func (r goodRepo) goodExecMethod(ctx context.Context) {
	r.db.Exec(ctx, "SELECT 1")
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

// badR3Repo has a pgExecutor field (like a real store) but misuses it.
type badR3Repo struct {
	db pgExecutor
}

// badR3PoolDirect is a RED R3 fixture: directly accesses r.db.pool.Exec,
// bypassing the ambient-tx routing. R3 must flag this.
func (r badR3Repo) badR3PoolDirect(ctx context.Context) {
	r.db.pool.Exec(ctx, "SELECT 1") // R3 violation: direct pool access
}

// badR3ExecDirect is a RED R3 fixture: calls r.db.ExecDirect outside the
// allowlist. R3 must flag this.
func (r badR3Repo) badR3ExecDirect(ctx context.Context) {
	r.db.ExecDirect(ctx, "SELECT 1") // R3 violation: ExecDirect outside allowlist
}

// goodApprovedSingleExecDirect is a GREEN R3(b) fixture: marker + single
// ExecDirect in the same approval scope (FuncDecl body, no nesting). R3
// must NOT flag this — marker presence covers the call.
func (r goodRepo) goodApprovedSingleExecDirect(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect("fixture-green-single")
	r.db.ExecDirect(ctx, "SELECT 1")
}

// goodApprovedMultiExecDirect is a GREEN R3(b) fixture: one marker covers
// multiple ExecDirect calls in the same approval scope. R3 must NOT flag
// either call — a single marker suffices for all ExecDirect calls within
// the same FuncDecl/FuncLit body.
func (r goodRepo) goodApprovedMultiExecDirect(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect("fixture-green-multi")
	r.db.ExecDirect(ctx, "SELECT 1")
	r.db.ExecDirect(ctx, "SELECT 2")
}

// badR3MarkerInNestedClosure: marker placed inside a nested FuncLit cannot
// approve an ExecDirect call in the outer FuncDecl scope. R3 must flag the
// outer call (the outer scope has no marker after F1 scope-bounded fix).
// (F1 in PR #917 round-2 review)
func (r badR3Repo) badR3MarkerInNestedClosure(ctx context.Context) {
	r.db.ExecDirect(ctx, "OUTER") // R3 violation: outer scope has no marker
	_ = func() {
		pgrepoapproved.ApprovedExecDirect("inner-marker-cannot-approve-outer")
	}
}

// badR3MarkerOuterExecInNestedClosure: outer-scope marker cannot approve an
// ExecDirect call inside a nested FuncLit scope. R3 must flag the inner call
// (the nested closure is its own approval scope and has no marker).
// (F1 in PR #917 round-2 review)
func (r badR3Repo) badR3MarkerOuterExecInNestedClosure(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect("outer-marker-cannot-approve-inner")
	_ = func() {
		r.db.ExecDirect(ctx, "INNER") // R3 violation: closure scope has no marker
	}
}

// badR3ApprovedConstIdent: marker reason is a const identifier (not a BasicLit).
// F2 rule 2 requires *ast.BasicLit + token.STRING; an *ast.Ident is rejected.
// (F2 in PR #917 round-2 review)
const fixtureReasonConst = "kebab-from-const"

func (r badR3Repo) badR3ApprovedConstIdent(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect(fixtureReasonConst) // F2: not a BasicLit
	r.db.ExecDirect(ctx, "SELECT 1")                      // R3 violation: marker rejected
}

// badR3ApprovedConcat: marker reason is "a" + "b" — *ast.BinaryExpr at the AST
// level, not *ast.BasicLit, so F2 rule 2 rejects it.
// (F2 in PR #917 round-2 review)
func (r badR3Repo) badR3ApprovedConcat(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect("ab-" + "cd-concat") // F2: BinaryExpr
	r.db.ExecDirect(ctx, "SELECT 1")                       // R3 violation
}

// badR3ApprovedEmpty: empty-string reason fails the kebab-case regex (which
// requires ^[a-z][a-z0-9-]+$ — length ≥ 2, leading lowercase letter).
// (F2 in PR #917 round-2 review)
func (r badR3Repo) badR3ApprovedEmpty(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect("") // F2: empty fails kebab regex
	r.db.ExecDirect(ctx, "SELECT 1")      // R3 violation
}

// badR3ApprovedPlaceholder: placeholder identifier ("todo") is rejected by the
// placeholder regex (todo/fixme/tbd/xxx/placeholder/wip) — meaningful reason
// required for audit trail.
// (F2 in PR #917 round-2 review)
func (r badR3Repo) badR3ApprovedPlaceholder(ctx context.Context) {
	pgrepoapproved.ApprovedExecDirect("todo") // F2: placeholder rejected
	r.db.ExecDirect(ctx, "SELECT 1")          // R3 violation
}
