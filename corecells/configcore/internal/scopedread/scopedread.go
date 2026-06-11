// Package scopedread funnels every tenant-scoped configcore read through a
// single tenant.WithScope + RunInTx wrapper. Under PR-3 row-level security
// (#1341) a SELECT on an RLS-protected table (config_entries / config_versions /
// feature_flags) returns 0 rows unless app.tenant_id is set, and that GUC is
// injected (SET LOCAL) only by TxManager.RunInTx. Config reads previously ran on
// raw pool connections, so each read slice must now run inside a tenant-scoped
// transaction; this package is the one place that wrapping lives.
package scopedread

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// Do scopes the context to tenant t (tenant.WithScope), runs fn inside a
// transaction via tx.RunInTx — so TxManager injects SET LOCAL app.tenant_id from
// that scope — and returns fn's result.
//
// This is the SOLE production caller of tenant.WithScope in configcore (PR-3a),
// pinned by archtest TENANT-TXSCOPE-WRITE-CALLER-01. Because the scope is set
// from the same TenantID t that the repo query filters on (WHERE tenant_id = $t),
// the RLS GUC and the application predicate are consistent by construction.
//
// fn must run all of its tenant-scoped DB work on the context it receives (the
// ambient-transaction context), not on the outer ctx, so the statements execute
// inside the SET LOCAL transaction.
func Do[T any](
	ctx context.Context,
	tx persistence.CellTxManager,
	t tenant.TenantID,
	fn func(ctx context.Context) (T, error),
) (T, error) {
	ctx = tenant.WithScope(ctx, t)
	var out T
	err := tx.RunInTx(ctx, func(txCtx context.Context) error {
		v, e := fn(txCtx)
		if e != nil {
			return e
		}
		out = v
		return nil
	})
	return out, err
}
