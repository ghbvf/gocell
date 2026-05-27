//go:build archtest_fixture

// fixture_repo.go holds R1 + R2 + R3 RED cases (and GREEN repo-layer
// controls). The _repo.go file extension triggers archtest R1/R2 scope; R3 is
// GLOBAL (call-bound approval) and applies regardless of filename — see
// fixture_service.go for the non-_repo.go R3 RED case proving global scope.
package pgrepoambienttxfixture

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
	"github.com/ghbvf/gocell/tools/archtest/internal/pgrepoambienttxfixture/internal/pgexec"
)

// badR1Repo holds a *pgxpool.Pool field in a _repo.go file. R1 must flag
// the field type position.
type badR1Repo struct { //nolint:unused // RED fixture
	pool *pgxpool.Pool
}

// goodRepo is the GREEN baseline: holds pgexec.PGExecutor (interface) instead
// of *pgxpool.Pool. R1 must NOT flag this.
type goodRepo struct { //nolint:unused // GREEN fixture
	db pgexec.PGExecutor
}

// badR2NonNew is a non-New* function with *pgxpool.Pool param. R2 must flag.
func badR2NonNew(pool *pgxpool.Pool) pgexec.PGExecutor { //nolint:unused // RED fixture
	return pgexec.New(pool)
}

// NewBadR2NoWrap is a New*-prefixed function with *pgxpool.Pool param that
// does NOT call pgexec.New. R2 must flag the func decl line.
func NewBadR2NoWrap(pool *pgxpool.Pool) struct{} { //nolint:unused // RED fixture
	_ = pool
	return struct{}{}
}

// badR2Unnamed has an UNNAMED *pgxpool.Pool param in a non-New* function.
// R2 must flag it (F1: unnamed pool params must not be silently dropped).
func badR2Unnamed(*pgxpool.Pool) { //nolint:unused // RED fixture
}

// NewBadR2Unnamed is a New* function with an UNNAMED *pgxpool.Pool param; it
// cannot wrap an unreferenceable param via pgexec.New. R2 must flag (F1).
func NewBadR2Unnamed(*pgxpool.Pool) struct{} { //nolint:unused // RED fixture
	return struct{}{}
}

// NewGoodRepo is the GREEN R2 control: wraps the pool via pgexec.New.
func NewGoodRepo(pool *pgxpool.Pool) pgexec.PGExecutor { //nolint:unused // GREEN fixture
	return pgexec.New(pool)
}

// fixtureReasonConst is a const identifier (not a callsite string literal),
// used to prove Approve(constIdent) is rejected by R3.
const fixtureReasonConst = "kebab-from-const"

// badR3ConstIdentReason: Approve's arg is a const identifier, not a BasicLit.
// R3 must flag the ExecDirect callsite.
func (r goodRepo) badR3ConstIdentReason(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve(fixtureReasonConst), r.db, ctx, "SELECT 1")
}

// badR3ConcatReason: Approve's arg is a "a"+"b" BinaryExpr, not a BasicLit.
func (r goodRepo) badR3ConcatReason(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve("ab-"+"cd-concat"), r.db, ctx, "SELECT 1")
}

// badR3EmptyReason: empty-string reason fails the kebab-case regex.
func (r goodRepo) badR3EmptyReason(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve(""), r.db, ctx, "SELECT 1")
}

// badR3PlaceholderReason: "todo" matches the placeholder reject regex.
func (r goodRepo) badR3PlaceholderReason(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve("todo"), r.db, ctx, "SELECT 1")
}

// badR3ReusedApproval: approval is pre-constructed into a variable and passed
// as an *ast.Ident, not the sanctioned inline Approve(literal) CallExpr. R3
// must flag (no sharing / reuse of approval tokens across callsites).
func (r goodRepo) badR3ReusedApproval(ctx context.Context) { //nolint:unused // RED fixture
	a := pgrepoapproved.Approve("reused-approval")
	_, _ = pgexec.ExecDirect(a, r.db, ctx, "SELECT 1")
}

// goodR3 is GREEN: inline Approve(kebab-literal) bound to the ExecDirect call.
func (r goodRepo) goodR3(ctx context.Context) { //nolint:unused // GREEN fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve("fixture-green-inline"), r.db, ctx, "SELECT 1")
}
