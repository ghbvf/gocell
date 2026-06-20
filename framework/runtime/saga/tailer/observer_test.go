package tailer

import (
	"context"
	"testing"
	"time"
)

// TestNopObserver_NoOp exercises every NopObserver method (coverage + ensures
// the default sink never panics on the hot path).
func TestNopObserver_NoOp(t *testing.T) {
	var o Observer = NopObserver{}
	ctx := context.Background()
	o.ObserveLockAcquire(ctx, testProj, LockContended)
	o.ObserveDrain(ctx, testProj, DrainOK)
	o.ObserveCheckpointAdvance(ctx, testProj, AdvanceStaleOwner)
	o.ObserveLag(ctx, testProj, 7)
	o.ObserveLastSuccess(ctx, testProj, time.Unix(1, 0))
}

// TestObserverEnumValues freezes the wire-facing string values (sibling to the
// SAGA-METRIC-LABEL-VALUES-FROZEN-01 archtest; a literal change here is a wire
// break the archtest also catches).
func TestObserverEnumValues(t *testing.T) {
	pairs := []struct{ got, want string }{
		{string(LockContended), "contended"},
		{string(LockCtxCanceled), "ctx_canceled"},
		{string(LockBackendError), "backend_error"},
		{string(DrainOK), "ok"},
		{string(DrainHeadError), "head_error"},
		{string(DrainStoreError), "store_error"},
		{string(DrainApplyError), "apply_error"},
		{string(AdvanceOK), "ok"},
		{string(AdvanceStaleOwner), "stale_owner"},
		{string(AdvanceError), "error"},
		{string(AdvancePoisonSkip), "poison_skip"},
	}
	for _, p := range pairs {
		if p.got != p.want {
			t.Errorf("enum value = %q, want %q", p.got, p.want)
		}
	}
}
