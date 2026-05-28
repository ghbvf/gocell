//go:build archtest_fixture

// fixture_repo.go holds R1 + R2 + R3 RED cases (and GREEN repo-layer
// controls). The _repo.go file extension triggers archtest scope for all
// three rules.
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

// badR2NonNew is a non-New* function with *pgxpool.Pool param in a _repo.go
// file. R2 must flag the func decl line.
func badR2NonNew(pool *pgxpool.Pool) pgexec.PGExecutor { //nolint:unused // RED fixture
	return pgexec.New(pool)
}

// NewBadR2NoWrap is a New*-prefixed function with *pgxpool.Pool param that
// does NOT call pgexec.New. R2 must flag the func decl line.
func NewBadR2NoWrap(pool *pgxpool.Pool) struct{} { //nolint:unused // RED fixture
	_ = pool
	return struct{}{}
}

// NewGoodRepo is the GREEN R2 control: wraps the pool via pgexec.New.
// R2 must NOT flag this.
func NewGoodRepo(pool *pgxpool.Pool) pgexec.PGExecutor { //nolint:unused // GREEN fixture
	return pgexec.New(pool)
}

// badR3ExecDirect calls pgexec.ExecDirect without a sibling marker. R3 must
// flag the call line. Note: ExecDirect is now a top-level function — there
// is no method form to bypass via local interface re-shape.
func (r goodRepo) badR3ExecDirect(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
}

// badR3MarkerInNestedClosure: marker placed inside a nested FuncLit cannot
// approve a pgexec.ExecDirect call in the outer FuncDecl scope. R3 must
// flag the outer call.
func (r goodRepo) badR3MarkerInNestedClosure(ctx context.Context) { //nolint:unused // RED fixture
	_, _ = pgexec.ExecDirect(r.db, ctx, "OUTER")
	_ = func() {
		pgrepoapproved.ApprovedExecDirect("inner-marker-cannot-approve-outer")
	}
}

// badR3MarkerOuterExecInNestedClosure: outer-scope marker cannot approve
// pgexec.ExecDirect inside a nested FuncLit. R3 must flag the inner call.
func (r goodRepo) badR3MarkerOuterExecInNestedClosure(ctx context.Context) { //nolint:unused // RED fixture
	pgrepoapproved.ApprovedExecDirect("outer-marker-cannot-approve-inner")
	_ = func() {
		_, _ = pgexec.ExecDirect(r.db, ctx, "INNER")
	}
}

// badR3ApprovedConstIdent: marker reason is a const identifier (not BasicLit).
const fixtureReasonConst = "kebab-from-const"

func (r goodRepo) badR3ApprovedConstIdent(ctx context.Context) { //nolint:unused // RED fixture
	pgrepoapproved.ApprovedExecDirect(fixtureReasonConst)
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
}

// badR3ApprovedConcat: marker reason is "a" + "b" BinaryExpr, not BasicLit.
func (r goodRepo) badR3ApprovedConcat(ctx context.Context) { //nolint:unused // RED fixture
	pgrepoapproved.ApprovedExecDirect("ab-" + "cd-concat")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
}

// badR3ApprovedEmpty: empty-string reason fails the kebab-case regex.
func (r goodRepo) badR3ApprovedEmpty(ctx context.Context) { //nolint:unused // RED fixture
	pgrepoapproved.ApprovedExecDirect("")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
}

// badR3ApprovedPlaceholder: "todo" matches the placeholder reject regex.
func (r goodRepo) badR3ApprovedPlaceholder(ctx context.Context) { //nolint:unused // RED fixture
	pgrepoapproved.ApprovedExecDirect("todo")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
}

// goodApprovedSingleExecDirect is GREEN: M=1 marker + E=1 ExecDirect, M==E.
func (r goodRepo) goodApprovedSingleExecDirect(ctx context.Context) { //nolint:unused // GREEN fixture
	pgrepoapproved.ApprovedExecDirect("fixture-green-single")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
}

// badR3SharedMarker is RED under per-callsite R3-Hard: M=1 marker + E=2
// ExecDirect calls is forbidden (sharing markers is not allowed). Both calls
// must be flagged (no callsite has its own dedicated marker). Used to live as
// goodApprovedMultiExecDirect under PR #917 scope-level semantics; round-3
// C4 changed semantic to 1:1 per-callsite Hard.
func (r goodRepo) badR3SharedMarker(ctx context.Context) { //nolint:unused // RED fixture
	pgrepoapproved.ApprovedExecDirect("fixture-red-shared-marker")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 2")
}

// goodApprovedTwoMarkersTwoExecDirect is GREEN: M=2 + E=2, M==E satisfies
// per-callsite 1:1 pairing. Each ExecDirect has its own dedicated marker.
func (r goodRepo) goodApprovedTwoMarkersTwoExecDirect(ctx context.Context) { //nolint:unused // GREEN fixture
	pgrepoapproved.ApprovedExecDirect("fixture-green-two-first")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 1")
	pgrepoapproved.ApprovedExecDirect("fixture-green-two-second")
	_, _ = pgexec.ExecDirect(r.db, ctx, "SELECT 2")
}
