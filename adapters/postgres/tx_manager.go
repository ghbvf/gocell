package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// Compile-time check: TxManager implements persistence.TxRunner.
var _ persistence.TxRunner = (*TxManager)(nil)

// savepointDepthKey tracks nested savepoint depth in context.
// savepointDepthKey is LOCAL to tx_manager (pg-specific nesting depth,
// no cross-package consumer). Contrast with persistence.TxCtxKey which
// is kernel-owned so that cells/*/internal/adapters/postgres can read
// the ambient tx without importing adapters/postgres.
type savepointDepthKey struct{}

// CtxWithTx returns a new context carrying the given pgx.Tx.
// Downstream code (e.g. OutboxWriter) retrieves it via
// persistence.TxFromContext[pgx.Tx]. Uses persistence.TxCtxKey so cell-local
// adapters can participate in ambient transactions without importing
// adapters/postgres.
func CtxWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, persistence.TxCtxKey, tx)
}

// savepointDepth returns the current savepoint nesting depth from context.
func savepointDepth(ctx context.Context) int {
	d, _ := ctx.Value(savepointDepthKey{}).(int)
	return d
}

// withSavepointDepth returns a context with the savepoint depth set.
func withSavepointDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, savepointDepthKey{}, depth)
}

// txAcquireMarker is an UNEXPORTED ctx marker that RunInTx stamps on the context
// it passes to pool.Begin. Because the type is package-private, no code outside
// this package can construct or forge it — so "marker present" ⟺ "this pool
// acquire originates from RunInTx" is a type-system Hard fact (same range-bound
// idiom as savepointDepthKey). The pgxpool BeforeAcquire guard (pool.go) uses it
// as defense-in-depth: a tenant-scoped ctx that acquires a connection WITHOUT
// this marker is a business path bypassing RunInTx's SET LOCAL, and is rejected.
type txAcquireMarker struct{}

// withTxAcquireIntent stamps the RunInTx acquire marker. Sole caller is RunInTx.
func withTxAcquireIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, txAcquireMarker{}, true)
}

// hasTxAcquireIntent reports whether ctx was stamped by withTxAcquireIntent.
func hasTxAcquireIntent(ctx context.Context) bool {
	v, _ := ctx.Value(txAcquireMarker{}).(bool)
	return v
}

// tenantScopeForTx resolves the tenant whose isolation boundary this transaction
// runs under, for the RLS GUC. Precedence (single source of truth — do not
// inline either lookup elsewhere):
//
//  1. tenant.ScopeFromContext — the dedicated PR-3 scope key, set by
//     tenant.WithScope at pre-auth / service / control-plane sites that carry no
//     authenticated-principal ctxkeys.TenantID.
//  2. ctxkeys.TenantIDFrom — the authenticated-principal carrier, already set by
//     the auth boundary for post-auth handlers and restored for event consumers.
//     The fallback lets those paths get their GUC for free, keeping the
//     WithScope caller-allowlist (TENANT-TXSCOPE-WRITE-CALLER-01) minimal.
//  3. absent — no tenant scope (probe / migration / tenant-less framework path).
//     The GUC is left unset; the RLS predicate sees NULL and returns 0 rows
//     (fail-closed), so a tenant-less path can never read tenant-scoped data.
func tenantScopeForTx(ctx context.Context) (tenant.TenantID, bool) {
	if t, ok := tenant.ScopeFromContext(ctx); ok {
		return t, true
	}
	if raw, ok := ctxkeys.TenantIDFrom(ctx); ok && raw != "" {
		// Defense-in-depth: re-validate/normalize the principal tenant via
		// ParseTenantID (same boundary tenant.FromContext uses) instead of a bare
		// cast, so a malformed ctxkeys value fails closed (skip → GUC unset → 0
		// rows) rather than reaching setLocalTenant as an aborting KindInternal.
		// The auth boundary already ParseTenantID's the claim, so this is a no-op
		// for legitimate values.
		if tid, err := tenant.ParseTenantID(raw); err == nil {
			return tid, true
		}
	}
	return "", false
}

// setLocalTenant injects the transaction-local RLS GUC app.tenant_id from the
// resolved tenant scope. It is the SOLE sanctioned writer of that GUC
// (TENANT-TXSCOPE / PG-SETLOCAL-FUNNEL-01 lock the callsite).
//
// set_config(name, value, is_local=true) is used rather than a literal
// `SET LOCAL app.tenant_id = '…'` because set_config is a planned function call
// that accepts a BIND PARAMETER ($1) — zero string interpolation, zero SQL
// injection surface — while is_local=true gives identical transaction-scoped,
// auto-reset-on-COMMIT/ROLLBACK semantics to SET LOCAL.
//
// Fail-closed: a present-but-non-canonical tenant scope aborts the transaction
// (it must never silently run with a malformed predicate). When no scope is
// present the GUC is left unset and the RLS policy's NULLIF(...)→NULL→0-rows
// branch enforces isolation.
func setLocalTenant(ctx context.Context, tx pgx.Tx) error {
	tid, present := tenantScopeForTx(ctx)
	if !present {
		return nil
	}
	if err := tid.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"tx: invalid tenant scope for RLS GUC injection", err)
	}
	return writeTenantGUC(ctx, tx, tid)
}

// writeTenantGUC is the single physical writer of the app.tenant_id RLS GUC. It
// lives in this file so PG-SETLOCAL-FUNNEL-01 (which locks the GUC-write string
// literal to tx_manager.go) stays satisfied — both setLocalTenant (tx-start, from
// the ctx scope) and ApplyTenantScope (mid-tx, explicit) route through here so the
// `set_config('app.tenant_id', $1, true)` literal appears exactly once.
func writeTenantGUC(ctx context.Context, tx pgx.Tx, tid tenant.TenantID) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tid.String()); err != nil {
		return classifyPGError(err, ErrAdapterPGQuery, "set local tenant scope")
	}
	return nil
}

// ApplyTenantScope sets the RLS app.tenant_id GUC on the AMBIENT transaction
// mid-flight. It exists for paths that cannot know the tenant at tx-start because
// they must read a non-RLS row inside the tx to learn it — specifically
// sessionrefresh, which Peeks the refresh token and reads sessions.tenant_id
// (neither table is under FORCE RLS) inside the cross-store tx (REFRESH-CROSS-
// STORE-TX-01), then derives the tenant and scopes the subsequent users/roles
// reads with this call.
//
// PRECONDITIONS (caller responsibility): (1) called inside a RunInTx (an ambient
// pgx.Tx must be in ctx, else this returns an error — it never silently runs
// unscoped); (2) every statement executed in the tx BEFORE this call touches only
// non-RLS tables. Calling it after an RLS-table statement would mean that earlier
// statement ran fail-closed (0 rows) — the late scope cannot retroactively fix it.
//
// is_local=true keeps the GUC transaction-scoped (auto-reset on COMMIT/ROLLBACK),
// identical to setLocalTenant.
func (tm *TxManager) ApplyTenantScope(ctx context.Context, canonicalTenantID string) error {
	tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
	if !ok {
		return errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
			"ApplyTenantScope: no ambient transaction (must be called inside RunInTx)")
	}
	// Re-validate (defense in depth): the typed pkg/tenant.TenantID is enforced at
	// the cells scopedtx funnel, but the kernel CellTxManager boundary is a string,
	// so parse it back to a canonical TenantID here before writing the GUC.
	tid, err := tenant.ParseTenantID(canonicalTenantID)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"ApplyTenantScope: invalid tenant scope for RLS GUC injection", err)
	}
	return writeTenantGUC(ctx, tx, tid)
}

// TxManager provides transactional execution with context-embedded pgx.Tx,
// savepoint-based nesting, and automatic panic rollback.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager creates a TxManager backed by the given Pool.
func NewTxManager(p *Pool) *TxManager {
	return &TxManager{pool: p.inner}
}

// RunInTx executes fn inside a database transaction. The pgx.Tx is stored in
// the context so that downstream code can retrieve it via
// persistence.TxFromContext[pgx.Tx].
//
// Nesting: if the context already carries a transaction, RunInTx creates a
// savepoint instead of a new top-level transaction. Savepoints are released on
// success and rolled back on error or panic.
//
// Panic safety: panics inside fn trigger a rollback (or savepoint rollback)
// before being re-raised.
func (tm *TxManager) RunInTx(ctx context.Context, fn func(ctx context.Context) error) (retErr error) {
	// Check for an existing transaction (nesting).
	if existingTx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tm.runInSavepoint(ctx, existingTx, fn)
	}

	// Start a new top-level transaction. The acquire ctx is stamped with the
	// RunInTx marker so the pgxpool BeforeAcquire guard (pool.go) can tell a
	// sanctioned tx acquire apart from a raw business pool checkout.
	tx, err := tm.pool.Begin(withTxAcquireIntent(ctx))
	if err != nil {
		return classifyPGConnectError(err, "begin tx")
	}

	// Inject the transaction-local RLS tenant GUC before any statement runs.
	// Top-level only: a nested savepoint reuses this tx, whose SET LOCAL already
	// holds (SET LOCAL is transaction-scoped). Fail-closed: a malformed scope
	// rolls back the just-opened tx rather than running unscoped.
	if err := setLocalTenant(ctx, tx); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			slog.Error("postgres: rollback after SET LOCAL tenant failure failed",
				slog.String("original_error", redaction.RedactError(err).Error()),
				slog.String("rollback_error", redaction.RedactError(rbErr).Error()),
			)
		}
		return err
	}

	txCtx := CtxWithTx(ctx, tx)
	txCtx = withSavepointDepth(txCtx, 0)
	// Install the after-commit registry on the outermost tx. drainAfterCommit is
	// true only here (the nested savepoint path never installs), so hooks fire
	// once after the durable commit below, not on savepoint RELEASE.
	txCtx, drainAfterCommit := persistence.WithAfterCommitRegistry(txCtx)
	afterCommitMark := persistence.AfterCommitMark(txCtx)

	// Panic recovery — rollback and re-panic.
	// Use context.WithoutCancel so rollback succeeds even if ctx is already canceled
	// (e.g. HTTP timeout). Without this, a canceled ctx causes rollback to fail,
	// leaving the transaction open until connection pool idle timeout.
	defer func() {
		if r := recover(); r != nil {
			persistence.TruncateAfterCommitTo(txCtx, afterCommitMark) // rolled-back scope: drop its hooks
			rbErr := tx.Rollback(context.WithoutCancel(ctx))
			if rbErr != nil {
				slog.Error("postgres: rollback after panic failed",
					slog.Any("panic", redaction.RedactAny(r)),
					slog.String("rollback_error", redaction.RedactError(rbErr).Error()),
				)
			}
			repanicAfterTopLevelTxRollback(r)
		}
	}()

	retErr = fn(txCtx)
	if retErr != nil {
		persistence.TruncateAfterCommitTo(txCtx, afterCommitMark) // rolled-back scope: drop its hooks
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			slog.Error("postgres: rollback failed",
				slog.String("original_error", redaction.RedactError(retErr).Error()),
				slog.String("rollback_error", redaction.RedactError(rbErr).Error()),
			)
		}
		return retErr
	}

	if err := tx.Commit(ctx); err != nil {
		return classifyPGError(err, ErrAdapterPGConnect, "commit tx")
	}
	if drainAfterCommit {
		persistence.RunAfterCommitHooks(txCtx)
	}
	return nil
}

// runInSavepoint executes fn within a savepoint on the existing transaction.
func (tm *TxManager) runInSavepoint(ctx context.Context, tx pgx.Tx, fn func(ctx context.Context) error) (retErr error) {
	depth := savepointDepth(ctx)
	spName := fmt.Sprintf("sp_%d", depth)

	if _, err := tx.Exec(ctx, fmt.Sprintf("SAVEPOINT %s", spName)); err != nil {
		return classifyPGError(err, ErrAdapterPGQuery, fmt.Sprintf("savepoint create savepoint=%s", spName))
	}

	nestedCtx := withSavepointDepth(ctx, depth+1)
	// Checkpoint the ambient after-commit registry: a savepoint rollback must
	// discard hooks this nested scope registers, even if the caller swallows the
	// error and the outermost tx commits.
	afterCommitMark := persistence.AfterCommitMark(nestedCtx)

	// Panic recovery — rollback savepoint and re-panic.
	// Use context.WithoutCancel so savepoint rollback succeeds even if ctx is canceled.
	defer func() {
		if r := recover(); r != nil {
			persistence.TruncateAfterCommitTo(nestedCtx, afterCommitMark)
			_, rbErr := tx.Exec(context.WithoutCancel(ctx), fmt.Sprintf("ROLLBACK TO SAVEPOINT %s", spName))
			if rbErr != nil {
				slog.Error("postgres: rollback savepoint after panic failed",
					slog.String("savepoint", spName),
					slog.Any("panic", redaction.RedactAny(r)),
					slog.String("rollback_error", redaction.RedactError(rbErr).Error()),
				)
			}
			repanicAfterSavepointRollback(r)
		}
	}()

	retErr = fn(nestedCtx)
	if retErr != nil {
		persistence.TruncateAfterCommitTo(nestedCtx, afterCommitMark)
		if _, rbErr := tx.Exec(context.WithoutCancel(ctx), fmt.Sprintf("ROLLBACK TO SAVEPOINT %s", spName)); rbErr != nil {
			slog.Error("postgres: rollback savepoint failed",
				slog.String("savepoint", spName),
				slog.String("original_error", redaction.RedactError(retErr).Error()),
				slog.String("rollback_error", redaction.RedactError(rbErr).Error()),
			)
		}
		return retErr
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf("RELEASE SAVEPOINT %s", spName)); err != nil {
		return classifyPGError(err, ErrAdapterPGQuery, fmt.Sprintf("savepoint release savepoint=%s", spName))
	}
	return nil
}

func repanicAfterTopLevelTxRollback(recovered any) {
	panic(panicregister.Approved("pg-tx-top-level-rollback-rethrow", recovered))
}

func repanicAfterSavepointRollback(recovered any) {
	panic(panicregister.Approved("pg-tx-savepoint-rollback-rethrow", recovered))
}
