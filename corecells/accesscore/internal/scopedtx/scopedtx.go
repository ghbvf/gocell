// Package scopedtx funnels every tenant-scoped accesscore write (and
// tenant-scoped read inside a transaction) through a single
// tenant.WithScope + RunInTx wrapper.  Under PR-3b row-level security
// (#1617) SELECT / INSERT / UPDATE / DELETE on the RLS-protected
// accesscore tables (users / roles / role_assignments) return 0 rows or
// fail-closed unless app.tenant_id is set, and that GUC is injected
// (SET LOCAL) only by TxManager.RunInTx.  This package is the one place
// that wrapping lives.
//
// sessions is deliberately NOT under RLS — it is the pre-auth tenant carrier
// read by PK before any scope is known. sessionrefresh therefore cannot use Do
// (scope-at-tx-start): it must Peek + read sessions.tenant_id inside the
// cross-store tx to learn the tenant, then scope mid-flight via ApplyScope.
package scopedtx

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// Do scopes the context to tenant t (tenant.WithScope), runs fn inside a
// transaction via tx.RunInTx — so TxManager injects SET LOCAL app.tenant_id
// from that scope — and returns fn's result.
//
// This is the SOLE production caller of tenant.WithScope in accesscore
// (PR-3b), pinned by archtest TENANT-TXSCOPE-WRITE-CALLER-01. Because the
// scope is set from the same TenantID t that the repo query filters on
// (WHERE tenant_id = $t), the RLS GUC and the application predicate are
// consistent by construction.
//
// fn must run all of its tenant-scoped DB work on the context it receives
// (the ambient-transaction context), not on the outer ctx, so the
// statements execute inside the SET LOCAL transaction.
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

// ApplyScope sets the RLS app.tenant_id GUC on the ALREADY-OPEN ambient
// transaction in ctx, mid-flight. Unlike Do (which scopes a fresh top-level tx
// at its start), ApplyScope is for the one path that cannot know the tenant at
// tx-start: sessionrefresh must Peek + read sessions.tenant_id (non-RLS tables)
// inside the cross-store tx (REFRESH-CROSS-STORE-TX-01) before it learns the
// tenant, then scope the subsequent users/roles reads with this call. ctx MUST
// already be inside the RunInTx whose tx is being scoped, and every prior
// statement in that tx MUST have touched only non-RLS tables. Non-RLS backends
// (mem / demo) no-op. Thin funnel over CellTxManager.ApplyTenantScope.
//
// t.String() converts tenant.TenantID → canonical UUID string: kernel/persistence
// defines CellTxManager with a plain string parameter to keep the kernel layer
// free of the pkg/tenant transitive dependency (kernel/ must not import cells/ or
// the broader platform packages). ApplyTenantScope on the postgres side re-validates
// the string as a UUID before writing the GUC.
func ApplyScope(ctx context.Context, tx persistence.CellTxManager, t tenant.TenantID) error {
	return tx.ApplyTenantScope(ctx, t.String())
}
