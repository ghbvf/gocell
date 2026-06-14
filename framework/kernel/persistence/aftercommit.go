package persistence

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

// AfterCommitHook is a best-effort, transient-only side effect run after a
// transaction's outermost commit succeeds. Typical hooks kick a saga
// dispatcher, invalidate a cache, flush a metric, or broadcast over a
// websocket — work that must observe a durable commit but must not be part of
// the transaction itself.
//
// A hook MUST NOT perform any persistent side effect: it must not touch the
// (now-committed) *pgx.Tx / *sql.Tx, nor write to an outbox.Writer. The tx is
// deliberately stripped from the ctx passed to a hook (see RunAfterCommitHooks),
// and archtest AFTERCOMMIT-HOOK-PURE-TRANSIENT-01 statically rejects hook
// bodies that reach for the tx or outbox writer.
//
// Failure semantics are best-effort: a panicking hook is recovered and logged
// at Warn, never propagated to the committing goroutine. This is a deliberate
// deviation from spring-tx, whose afterCommit propagates exceptions — the commit
// is already durable, so a transient-hook failure must not surface as a request
// error or trigger a rollback. See
// docs/architecture/202605230300-adr-aftercommit-hook-narrow-scope.md.
//
// Hooks run without a deadline (context.WithoutCancel); a hook doing network I/O
// or heavy work MUST install its own timeout (context.WithTimeout) so it cannot
// block the committing goroutine — and thus RunInTx's return — indefinitely.
//
// Example (saga dispatcher kick — the canonical use):
//
//	persistence.RegisterAfterCommit(ctx, func(ctx context.Context) {
//	    dispatcher.Kick(ctx) // ctx still carries trace/request-id; the tx is stripped
//	})
//
// Spring-tx analog: TransactionSynchronization.afterCommit(); registration is
// persistence.RegisterAfterCommit (≙ TransactionSynchronizationManager.registerSynchronization).
type AfterCommitHook func(ctx context.Context)

// afterCommitRegistryKey is the context key under which the per-transaction
// hook registry is stored. It is intentionally distinct from TxCtxKey: the
// registry carries no driver dependency and outlives the tx by exactly the
// post-commit drain.
type afterCommitRegistryKey struct{}

// afterCommitRegistry accumulates hooks registered during a single RunInTx
// scope. A transaction's fn executes on one goroutine, so registration and the
// RunInTx-side drain never race; no mutex is required.
type afterCommitRegistry struct {
	hooks    []AfterCommitHook
	draining bool
}

// RegisterAfterCommit appends hook to the after-commit registry installed by
// the enclosing RunInTx. It is the sole public entry point for scheduling
// post-commit work; call it from within a RunInTx fn body.
//
// Calling RegisterAfterCommit outside a managed transaction (no ambient
// registry) is a programmer error and panics through the approved funnel —
// scheduling a post-commit side effect with no commit to follow would silently
// drop the effect. (spring-tx's registerSynchronization likewise throws
// IllegalStateException when no synchronization is active.) The panic
// propagates up the stack; in an HTTP request it is recovered by the framework
// Recovery middleware as a 500. In tests, assert it with require.Panics.
//
// A nil hook is ignored. A hook registered while hooks are draining is ignored
// (snapshot semantics; there is no second round).
func RegisterAfterCommit(ctx context.Context, hook AfterCommitHook) {
	reg, ok := ctx.Value(afterCommitRegistryKey{}).(*afterCommitRegistry)
	if !ok || reg == nil {
		panic(panicregister.Approved("aftercommit-no-active-tx",
			errcode.Assertion("persistence: RegisterAfterCommit called outside an active transaction")))
	}
	if hook == nil || reg.draining {
		return
	}
	reg.hooks = append(reg.hooks, hook)
}

// AfterCommitMark returns the number of hooks currently in the ambient registry
// — a checkpoint a RunInTx scope captures before running its fn so it can
// discard exactly the hooks fn registers if fn fails (see TruncateAfterCommitTo).
// Returns 0 when no registry is present.
func AfterCommitMark(ctx context.Context) int {
	reg, ok := ctx.Value(afterCommitRegistryKey{}).(*afterCommitRegistry)
	if !ok || reg == nil {
		return 0
	}
	return len(reg.hooks)
}

// TruncateAfterCommitTo discards every hook registered after mark — the hooks of
// a RunInTx scope whose fn returned an error or panicked. The scope's unit of
// work was rolled back (a PG savepoint, or simply not committed), so the premise
// of its after-commit side effects no longer holds and they must not fire even
// if an enclosing scope swallows the error and commits.
//
// Sole callers are TxRunner implementations on their fn-failure path (archtest
// AFTERCOMMIT-HOOK-PURE-TRANSIENT-01/A3 caller allowlist). A no-op when no
// registry is present or mark is out of [0, len].
func TruncateAfterCommitTo(ctx context.Context, mark int) {
	reg, ok := ctx.Value(afterCommitRegistryKey{}).(*afterCommitRegistry)
	if !ok || reg == nil || mark < 0 || mark > len(reg.hooks) {
		return
	}
	reg.hooks = reg.hooks[:mark]
}

// WithAfterCommitRegistry installs a fresh after-commit registry into ctx
// unless one is already present (a nested RunInTx). The bool reports whether
// THIS call installed the registry: only the outermost installer is
// responsible for draining via RunAfterCommitHooks, so nested hooks accumulate
// into the outer registry and fire once after the outermost commit.
//
// Sole callers are TxRunner implementations (archtest
// AFTERCOMMIT-HOOK-PURE-TRANSIENT-01/A3 caller allowlist).
func WithAfterCommitRegistry(ctx context.Context) (context.Context, bool) {
	if _, ok := ctx.Value(afterCommitRegistryKey{}).(*afterCommitRegistry); ok {
		return ctx, false
	}
	return context.WithValue(ctx, afterCommitRegistryKey{}, &afterCommitRegistry{}), true
}

// RunAfterCommitHooks drains and synchronously runs the registered hooks on the
// committing goroutine. It must be called only by the outermost RunInTx, and
// only after a durable commit.
//
// Each hook runs under context.WithoutCancel so it survives the request ctx
// being canceled (e.g. an HTTP handler that has already returned), and with the
// ambient tx stripped (TxCtxKey shadowed) so the committed tx is unreachable
// through the passed ctx. Each hook is recovered independently: a panic in one
// hook is logged and does not stop the others or escape to the caller.
//
// Draining a ctx with no registry is a safe no-op.
func RunAfterCommitHooks(ctx context.Context) {
	reg, ok := ctx.Value(afterCommitRegistryKey{}).(*afterCommitRegistry)
	if !ok || reg == nil {
		return
	}
	reg.draining = true
	hooks := reg.hooks
	reg.hooks = nil

	// Strip the ambient tx and detach from cancelation. Trace / request-id keys
	// live under different keys and survive.
	hookCtx := context.WithoutCancel(context.WithValue(ctx, TxCtxKey, nil))
	for i, h := range hooks {
		runAfterCommitHook(hookCtx, i, h)
	}
}

// runAfterCommitHook executes a single hook with panic isolation.
func runAfterCommitHook(ctx context.Context, index int, hook AfterCommitHook) {
	defer func() {
		if r := recover(); r != nil {
			// Warn, not Error: the commit is durable, so a skipped transient side
			// effect is degraded operation, not a correctness failure (per
			// observability.md). WarnContext lets a context-aware slog handler
			// attach request/trace correlation from the (still-populated) ctx.
			slog.WarnContext(ctx, "persistence: after-commit hook panicked; side effect skipped (commit is durable)",
				slog.Int("hook_index", index),
				slog.Any("panic", redaction.RedactAny(r)),
			)
		}
	}()
	hook(ctx)
}
