//go:build archtest_fixture

// fixture_repo.go holds R1 + R2 + R3 RED cases (and GREEN repo-layer
// controls). The _repo.go file extension triggers archtest R1/R2 scope; R3 is
// GLOBAL (call-bound approval) and applies regardless of filename — see
// fixture_service.go for the non-_repo.go R3 RED case proving global scope.
package pgrepoambienttxfixture

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/framework/pkg/pgrepoapproved"
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

// fixtureLocalReason is a locally-declared ApprovalReason constant in the
// fixture package. Although its underlying type matches the catalog newtype,
// archtest R3 must reject Approve(fixtureLocalReason) because the constant is
// not declared in the sanctioned pkg/pgrepoapproved package — only catalog
// constants minted there are reachable approvals.
const fixtureLocalReason pgrepoapproved.ApprovalReason = "fixture-local-reason" //nolint:unused // RED fixture

// badR3LocalConst: Approve's arg is a locally-declared ApprovalReason const
// outside pkg/pgrepoapproved. R3 must flag the ExecDirect callsite — gate #5
// checks the const's package path equals the canonical catalog path.
func (r goodRepo) badR3LocalConst(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve(fixtureLocalReason), r.db, ctx, "SELECT 1")
}

// badR3TypeConversion: Approve's arg is a type conversion expression
// (pgrepoapproved.ApprovalReason("…")) rather than a declared catalog const.
// R3 must flag — gate #4 checks the arg resolves to *types.Const.
func (r goodRepo) badR3TypeConversion(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve(pgrepoapproved.ApprovalReason("type-conv-orphan")), r.db, ctx, "SELECT 1")
}

// badR3ReusedApproval: approval is pre-constructed into a variable and passed
// as an *ast.Ident, not the sanctioned inline Approve(literal) CallExpr. R3
// must flag (no sharing / reuse of approval tokens across callsites).
func (r goodRepo) badR3ReusedApproval(ctx context.Context) { //nolint:unused // RED fixture
	a := pgrepoapproved.Approve(pgrepoapproved.RevokeSessionCascade)
	_, _ = pgexec.ExecDirect(a, r.db, ctx, "SELECT 1")
}

// goodR3 is GREEN: inline Approve(catalog-const) bound to the ExecDirect call.
func (r goodRepo) goodR3(ctx context.Context) { //nolint:unused // GREEN fixture
	_, _ = pgexec.ExecDirect(pgrepoapproved.Approve(pgrepoapproved.RevokeSessionCascade), r.db, ctx, "SELECT 1")
}

// badR4OrphanDiscarded: orphan Approve callsite — return value goes to the
// blank identifier, never reaches an ExecDirect. R4 must flag.
func badR4OrphanDiscarded() { //nolint:unused // RED fixture
	_ = pgrepoapproved.Approve(pgrepoapproved.IntegrationTestLockUser)
}

// badR4OrphanAssigned: orphan Approve callsite — assigned to a local variable
// that is never passed to ExecDirect. R4 must flag (the Approve call itself
// is the orphan, regardless of what happens to the resulting variable).
func badR4OrphanAssigned() { //nolint:unused // RED fixture
	a := pgrepoapproved.Approve(pgrepoapproved.IntegrationTestDeleteUser)
	_ = a
}
