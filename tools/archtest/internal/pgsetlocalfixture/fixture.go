//go:build archtest_fixture

// Package pgsetlocalfixture is an archtest RED fixture for PG-SETLOCAL-FUNNEL-01,
// covering BOTH prongs:
//
//   - Prong 1 (bare-SET tripwire): badBareSet is a bare session-scope `SET` that
//     MUST be reported (a session GUC survives the connection's return to the pool
//     and leaks across requests); goodSetLocal uses the allowed `SET LOCAL` form
//     and MUST NOT be reported. The prong-1 scanner asserts exactly one diagnostic.
//   - Prong 2 (app.tenant_id GUC-write funnel): badSetConfigGUCWrite is the
//     canonical production GUC-write form, set_config('app.tenant_id', …), placed
//     OUTSIDE the sanctioned writer file. All three calls write app.tenant_id, so
//     the prong-2 scanner (run with the production writer file, which never matches
//     a fixture rel) must report all three (#1622 F4).
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

// goodSetLocal is the GREEN control for prong 1: SET LOCAL is transaction-scoped
// and allowed (not a bare SET). It IS an app.tenant_id GUC write, so prong 2 still
// reports it (it is outside the sanctioned writer file).
func goodSetLocal(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SET LOCAL app.tenant_id = 'x'")
}

// badSetConfigGUCWrite is the prong-2 RED case: the canonical production GUC-write
// form (set_config) targeting app.tenant_id, outside the sanctioned writer file —
// the prong-2 scanner MUST report it. It is NOT a bare `SET …` statement (it starts
// with SELECT), so prong 1 deliberately does not flag it.
func badSetConfigGUCWrite(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", "00000000-0000-0000-0000-000000000000")
}
