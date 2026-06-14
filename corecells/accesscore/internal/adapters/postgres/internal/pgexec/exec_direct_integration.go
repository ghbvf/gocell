//go:build integration

// exec_direct_integration.go gates accesscore's ExecDirect top-level function
// behind the `integration` build tag so it never appears on the production
// API surface. accesscore production code has zero ExecDirect callsites; only
// integration tests (role_repo_integration_test.go) need to drive raw SQL
// past the ambient-tx routing to exercise DB-level triggers and cascade
// invariants. Default builds (no `-tags=integration`) do not compile this
// file, keeping the test-only surface invisible to production callers.
//
// See pgexec.go's package godoc for the per-cell ExecDirect exposure policy.

package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/pgrepoapproved"
)

// ExecDirect bypasses the ambient transaction. The first parameter is a
// call-bound pgrepoapproved.Approval token (mint inline via
// pgrepoapproved.Approve(<catalog-const>)). Sealed top-level function — see
// adapters/postgres/internal/pgexec.ExecDirect for full design rationale.
//
// This function is build-tag-gated to `integration`: only integration tests
// (which compile with `-tags=integration`) can reach it. Production callers
// outside this sub-package cannot reference ExecDirect because it does not
// appear in default builds.
func ExecDirect(_ pgrepoapproved.Approval, e PGExecutor, ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	impl, ok := e.(*pgExecutor)
	if !ok {
		// Unreachable in production: PGExecutor is sealed, so every value is
		// *pgExecutor. A non-*pgExecutor here is an in-package-test-only
		// programmer error — A-class assertion panic, not an error return.
		panic(panicregister.Approved("pgexec-execdirect-non-sealed",
			errcode.Assertion("pgexec.ExecDirect: PGExecutor must originate from pgexec.New")))
	}
	return impl.pool.Exec(ctx, sql, args...)
}
