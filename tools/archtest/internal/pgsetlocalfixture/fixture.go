//go:build archtest_fixture

// Package pgsetlocalfixture is an archtest RED fixture for PG-SETLOCAL-FUNNEL-01
// (Prong 1, the bare-SET tripwire). It contains one `.Exec` call with a bare
// session-scope `SET` (which MUST be reported — a session GUC survives the
// connection's return to the pool and leaks across requests) and one with the
// allowed `SET LOCAL` form (which MUST NOT be reported). The archtest scans this
// package with the same scanner it runs over adapters/postgres production and
// asserts exactly one diagnostic.
package pgsetlocalfixture

import "context"

// fakeTx mimics the pgx.Tx.Exec shape (ctx, sql, args...) so the AST scanner —
// which matches a call to a method named Exec with a SET-prefixed string-literal
// argument — fires on the calls below without needing a real pgx dependency.
type fakeTx struct{}

func (fakeTx) Exec(ctx context.Context, sql string, args ...any) (any, error) { return nil, nil }

// badBareSet is the RED case: a bare session-scope SET that the funnel forbids.
func badBareSet(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SET app.tenant_id = 'x'")
}

// goodSetLocal is the GREEN control: SET LOCAL is transaction-scoped and allowed.
func goodSetLocal(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SET LOCAL app.tenant_id = 'x'")
}
