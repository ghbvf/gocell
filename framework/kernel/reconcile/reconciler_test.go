package reconcile

import (
	"context"
	"testing"
	"time"
)

// reconcilerSampleRequeue is a representative RequeueAfter value for the
// interface-shape tests (TEST-TIME-LITERAL-01: extracted to a package-level const).
const reconcilerSampleRequeue = 5 * time.Second

// TestRequest_EntityIDOnly asserts Request carries exactly the EntityID locator
// and nothing else is needed to construct it (the minimal-core promise: no
// NamespacedName, no priority).
func TestRequest_EntityIDOnly(t *testing.T) {
	t.Parallel()
	r := Request{EntityID: "entity-1"}
	if r.EntityID != "entity-1" {
		t.Fatalf("Request.EntityID = %q, want %q", r.EntityID, "entity-1")
	}
}

// TestResult_ZeroValueIsDefaultTick documents the FR-002 zero-value semantic:
// a Result{} (RequeueAfter == 0) means "requeue at the default tick interval",
// not "do not requeue". The Loop reads RequeueAfter == 0 as default-tick.
func TestResult_ZeroValueIsDefaultTick(t *testing.T) {
	t.Parallel()
	var res Result
	if res.RequeueAfter != 0 {
		t.Fatalf("zero Result.RequeueAfter = %v, want 0 (== default tick)", res.RequeueAfter)
	}
}

// fakeReconciler is an out-of-shape consumer implementation proving the
// interface is satisfiable by an arbitrary external type (the SC-001
// minimal-interface promise).
type fakeReconciler struct {
	calls int
	res   Result
	err   error
}

func (f *fakeReconciler) Reconcile(_ context.Context, _ Request) (Result, error) {
	f.calls++
	return f.res, f.err
}

// Compile-time proof the minimal interface is satisfiable.
var _ Reconciler = (*fakeReconciler)(nil)

// TestReconciler_InterfaceSatisfiable exercises the interface through the
// Reconciler abstraction (not the concrete type) to lock the call shape
// Reconcile(ctx, Request) (Result, error).
func TestReconciler_InterfaceSatisfiable(t *testing.T) {
	t.Parallel()
	var r Reconciler = &fakeReconciler{res: Result{RequeueAfter: reconcilerSampleRequeue}}
	got, err := r.Reconcile(context.Background(), Request{EntityID: "x"})
	if err != nil {
		t.Fatalf("Reconcile err = %v, want nil", err)
	}
	if got.RequeueAfter != reconcilerSampleRequeue {
		t.Fatalf("Result.RequeueAfter = %v, want %v", got.RequeueAfter, reconcilerSampleRequeue)
	}
}
