package reconcile

import (
	"context"
	"fmt"
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
func recoverReconcile(ctx context.Context, rec Reconciler, req Request) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reconcile: recovered panic in Reconcile(entity=%q): %v", req.EntityID, r)
			res = Result{}
		}
	}()
	return rec.Reconcile(ctx, req)
}
