package tenant

import "context"

// scopeKey is the DEDICATED PR-3 transaction tenant-scope ctx key. It is an
// UNEXPORTED type, so no package outside pkg/tenant can construct it, read it
// directly, alias it, or forge a value under it — the only write path is the
// exported WithScope below (sealed-construction Hard downstream).
//
// It is deliberately SEPARATE from ctxkeys.TenantID (the authenticated-principal
// carrier, written only at the auth boundary per CTXKEYS-PRINCIPAL-WRITE-CALLER-01).
// The RLS GUC injection in adapters/postgres.RunInTx reads ScopeFromContext
// FIRST, then falls back to ctxkeys.TenantID for the already-set principal path
// (post-auth handlers / restored consumers). The dedicated key lets pre-auth /
// service / control-plane sites declare a tx-scoped tenant for the RLS
// `SET LOCAL app.tenant_id` GUC WITHOUT minting a principal tenant they do not
// have — keeping the principal key meaning strictly "authenticated JWT tenant".
type scopeKey struct{}

// WithScope returns a context carrying t as the transaction RLS tenant scope.
//
// WRITE-SIDE TRUST BOUNDARY: the value placed here is what RunInTx interpolates
// into the per-transaction RLS GUC (`set_config('app.tenant_id', …, true)`), so
// WithScope is the tenant-isolation write boundary for any path that does not
// already carry an authenticated-principal ctxkeys.TenantID. Its production
// callers are pinned by archtest TENANT-TXSCOPE-WRITE-CALLER-01. Business cells
// MUST NOT call it for ordinary post-auth work — there the tenant flows from the
// authenticated principal (the ctxkeys.TenantID fallback). The sanctioned
// callers are pre-auth derivation sites and the internal control-plane read path.
//
// t is stored as-is; RunInTx defensively Validate()s it before use and fails
// closed on any non-canonical value, so a malformed scope can never reach the
// GUC.
func WithScope(ctx context.Context, t TenantID) context.Context {
	return context.WithValue(ctx, scopeKey{}, t)
}

// ScopeFromContext returns the transaction tenant scope and whether one is
// present. The read side is unrestricted (mirrors the ctxkeys XxxIDFrom getters);
// only the write side (WithScope) is funneled.
func ScopeFromContext(ctx context.Context) (TenantID, bool) {
	t, ok := ctx.Value(scopeKey{}).(TenantID)
	return t, ok
}
