package reconcile

import (
	"context"
	"fmt"
	"log/slog"
)

// classify maps an error to its metric result label. It is the single source
// of classification truth for the Loop's process() switch — the loop agent
// will delete (*Loop).safeReconcile and call recoverReconcile + classify
// instead.
//
// Mapping:
//   - nil   → resultSuccess
//   - IsPermanent(err) → resultPermanent
//   - otherwise        → resultTransient
func classify(err error) string {
	if err == nil {
		return resultSuccess
	}
	if IsPermanent(err) {
		return resultPermanent
	}
	return resultTransient
}

// recoverReconcile invokes rec.Reconcile(ctx, req) with panic recovery so a
// single entity's panic cannot crash the worker goroutine. A recovered panic is
// returned as a transient (non-permanent) error that includes the entity ID and
// the recovered value. On no panic, the result of rec.Reconcile is returned
// verbatim.
//
// The returned panic error always satisfies classify(err) == resultTransient
// (it is never permanent, so the Loop retries rather than dead-lettering).
// This is an A-class recovery: the worker has no upstream recovery boundary,
// so the panic is absorbed and reported as a transient failure rather than
// re-panicked.
//
// O3: a recovered panic is a reconciler BUG and is logged at Error level here
// (correctness-affecting; per observability.md Error = "influences correctness").
// The metric result label stays "transient" — no 5th label is added.
func recoverReconcile(ctx context.Context, rec Reconciler, req Request, logger *slog.Logger, reconcilerID string) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reconcile: recovered panic in Reconcile(entity=%q): %v", req.EntityID, r)
			res = Result{}
			// Log at Error: a reconciler panic is a correctness bug, not ordinary
			// transient noise. The entity will be requeued (transient semantics), but
			// the operator must see this in the error log, not just the metric.
			logger.ErrorContext(ctx, "reconcile: reconciler panicked (BUG); entity requeued as transient",
				slog.String("reconciler", reconcilerID),
				slog.String("entity", req.EntityID),
				slog.Any("error", err))
		}
	}()
	return rec.Reconcile(ctx, req)
}
