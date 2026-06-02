package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicReconciler always panics with the given value.
type panicReconciler struct{ payload any }

func (p panicReconciler) Reconcile(_ context.Context, _ Request) (Result, error) {
	panic(p.payload)
}

// successReconciler always returns a fixed Result and nil error.
type successReconciler struct{ result Result }

func (s successReconciler) Reconcile(_ context.Context, _ Request) (Result, error) {
	return s.result, nil
}

// testLogger returns a slog.Logger that writes JSON to buf.
func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestRecovery_PanicConvertsToError verifies that a panicking Reconciler
// produces a non-nil error and a zero Result, without re-panicking.
func TestRecovery_PanicConvertsToError(t *testing.T) {
	t.Parallel()
	rec := panicReconciler{payload: "kaboom"}
	req := Request{EntityID: "e1"}

	res, err := recoverReconcile(context.Background(), rec, req, slog.Default(), "test_reconciler")

	require.Error(t, err, "panic must be converted to an error")
	assert.Equal(t, Result{}, res, "Result must be zero on panic")
	assert.False(t, IsPermanent(err), "panic error must not be permanent (must be transient/retryable)")
	assert.Contains(t, err.Error(), "e1", "error must mention the entity ID")
}

// TestRecovery_PanicMetricRecorded verifies that classify(errFromPanic)
// returns resultTransient — proving the recovered panic never creates a 5th
// metric label.
func TestRecovery_PanicMetricRecorded(t *testing.T) {
	t.Parallel()
	rec := panicReconciler{payload: "boom"}
	req := Request{EntityID: "e-metric"}

	_, err := recoverReconcile(context.Background(), rec, req, slog.Default(), "test_reconciler")

	require.Error(t, err)
	label := classify(err)
	assert.Equal(t, resultTransient, label,
		"classify(panicErr) must be resultTransient (never a 5th result label)")
}

// TestRecovery_OtherEntityNotAffected verifies that recoverReconcile is
// call-scoped: a panicking call for one entity does not affect a subsequent
// call for a different entity.
func TestRecovery_OtherEntityNotAffected(t *testing.T) {
	t.Parallel()
	pRec := panicReconciler{payload: "boom"}
	req1 := Request{EntityID: "boom-entity"}
	_, err1 := recoverReconcile(context.Background(), pRec, req1, slog.Default(), "test_reconciler")
	require.Error(t, err1, "first call must surface the panic as an error")

	// Arbitrary non-zero RequeueAfter; the value is immaterial — this only
	// checks the Result passes through recoverReconcile unchanged.
	want := Result{RequeueAfter: defaultBackoffBase}
	sRec := successReconciler{result: want}
	req2 := Request{EntityID: "ok-entity"}
	res2, err2 := recoverReconcile(context.Background(), sRec, req2, slog.Default(), "test_reconciler")
	require.NoError(t, err2, "second reconciler must be unaffected")
	assert.Equal(t, want, res2, "second reconciler must return its real Result")
}

// TestRecovery_PanicLogsAtErrorLevel verifies that a recovered panic produces
// an Error-level log entry containing the entity ID and reconciler ID, so
// operators see it distinctly from ordinary transient noise (O3).
func TestRecovery_PanicLogsAtErrorLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := testLogger(&buf)

	rec := panicReconciler{payload: "oh no"}
	req := Request{EntityID: "panicking-entity"}

	_, err := recoverReconcile(context.Background(), rec, req, logger, "my_reconciler")
	require.Error(t, err)

	logOutput := buf.String()
	assert.Contains(t, logOutput, `"level":"ERROR"`, "panic must be logged at Error level")
	assert.Contains(t, logOutput, "panicking-entity", "log must include entity ID")
	assert.Contains(t, logOutput, "my_reconciler", "log must include reconciler ID")
}

// TestClassify verifies the classify helper's full table:
//   - nil → resultSuccess
//   - PermanentError(x) → resultPermanent
//   - plain error → resultTransient
//   - wrapped PermanentError → resultPermanent
func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want resultLabel
	}{
		{"nil", nil, resultSuccess},
		{"permanent", PermanentError(errors.New("bad")), resultPermanent},
		{"transient plain", errors.New("oops"), resultTransient},
		{"wrapped permanent", errors.Join(errors.New("ctx"), PermanentError(errors.New("bad"))), resultPermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestClassify_WrappedPermanentViaErrorf verifies that classify recognizes a
// PermanentError wrapped with fmt.Errorf("%w").
func TestClassify_WrappedPermanentViaErrorf(t *testing.T) {
	t.Parallel()
	inner := PermanentError(errors.New("revoked cert"))
	wrapped := fmt.Errorf("operation failed: %w", inner)
	assert.Equal(t, resultPermanent, classify(wrapped))
}

// TestRecovery_FencedStaleClassifiedPermanent verifies that ErrFencedWriteStale is
// classified as resultPermanent (NOT transient). Per fenced.go godoc: stale-epoch
// write rejected by the fencing CAS is not a retry — a fresh trigger under the new
// leader re-observes the entity.
func TestRecovery_FencedStaleClassifiedPermanent(t *testing.T) {
	t.Parallel()
	label := classify(ErrFencedWriteStale)
	assert.Equal(t, resultPermanent, label,
		"ErrFencedWriteStale must classify as resultPermanent (fencing race, not transient retry)")
}

// TestRecovery_FencedStaleWrappedClassifiedPermanent verifies the wrapped form.
func TestRecovery_FencedStaleWrappedClassifiedPermanent(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("reconcile: fenced write (entity=%q epoch=%d): %w", "dev-1", uint64(3), ErrFencedWriteStale)
	assert.Equal(t, resultPermanent, classify(wrapped),
		"wrapped ErrFencedWriteStale must also classify as resultPermanent")
}
