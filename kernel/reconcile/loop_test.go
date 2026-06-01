package reconcile

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// shortRequeue is a fast requeue interval used by tests that observe requeue
// behavior (transient retry / permanent dead-letter) without long waits.
const shortRequeue = 20 * testtime.D1ms

// funcReconciler adapts a function to the Reconciler interface.
type funcReconciler func(ctx context.Context, req Request) (Result, error)

func (f funcReconciler) Reconcile(ctx context.Context, req Request) (Result, error) {
	return f(ctx, req)
}

// blockingReconciler blocks each Reconcile on a release channel and tracks
// global / per-entity concurrency so tests can assert the worker-pool bound and
// same-entity serialization.
type blockingReconciler struct {
	release   chan struct{}
	ignoreCtx bool // when true, block on release only (simulate a stuck reconcile)
	result    Result
	err       error

	mu            sync.Mutex
	concurrent    int
	maxConcurrent int
	perEntity     map[string]int
	maxPerEntity  int
	calls         atomic.Int64

	// firstCallOnce and firstCallSig together form a deterministic signal:
	// firstCallSig is closed the first time Reconcile is entered. Tests that
	// need to know "at least one reconcile has started" use
	// testwait.Deterministic(t, r.firstCallSig) instead of polling calls.Load.
	firstCallOnce sync.Once
	firstCallSig  chan struct{}
}

func newBlockingReconciler() *blockingReconciler {
	return &blockingReconciler{
		release:      make(chan struct{}),
		perEntity:    map[string]int{},
		result:       Result{RequeueAfter: testtime.D1h}, // requeue far out — no re-fire mid-test
		firstCallSig: make(chan struct{}),
	}
}

func (r *blockingReconciler) Reconcile(ctx context.Context, req Request) (Result, error) {
	r.calls.Add(1)
	r.firstCallOnce.Do(func() { close(r.firstCallSig) })
	r.enter(req.EntityID)
	defer r.exit(req.EntityID)
	if r.ignoreCtx {
		<-r.release
	} else {
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	}
	return r.result, r.err
}

func (r *blockingReconciler) enter(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.concurrent++
	if r.concurrent > r.maxConcurrent {
		r.maxConcurrent = r.concurrent
	}
	r.perEntity[id]++
	if r.perEntity[id] > r.maxPerEntity {
		r.maxPerEntity = r.perEntity[id]
	}
}

func (r *blockingReconciler) exit(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.concurrent--
	r.perEntity[id]--
}

func (r *blockingReconciler) currentConcurrent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.concurrent
}

func (r *blockingReconciler) snapshot() (maxConcurrent, maxPerEntity int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxConcurrent, r.maxPerEntity
}

// notReadyReconciler implements the optional readiness gate and fails it.
type notReadyReconciler struct{ funcReconciler }

func (notReadyReconciler) Validate() error { return errors.New("not constructed properly") }

func startCtxs(t *testing.T) (ownerCtx context.Context, ownerCancel context.CancelFunc) {
	t.Helper()
	return context.WithCancel(context.Background())
}

func stopCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), testtime.CtxShort)
}

// -----------------------------------------------------------------------------
// Construction / readiness gates
// -----------------------------------------------------------------------------

func TestLoop_NilReconcilerFailsStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	l := &Loop{ReconcilerID: "rc"}
	err := l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil Reconciler")
	require.NoError(t, l.Stop(context.Background())) // no-op, goleak stays clean
}

func TestLoop_ReconcilerNotReadyFailsStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	l := &Loop{ReconcilerID: "rc", Reconciler: notReadyReconciler{}}
	err := l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")
	require.NoError(t, l.Stop(context.Background()))
}

func TestLoop_BadMetricLabelsFailsStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	p := newRecordingProvider()
	bad, err := p.CounterVec(kernelmetrics.CounterOpts{Name: metricReconcileTotal, LabelNames: []string{"wrong"}})
	require.NoError(t, err)
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{}, nil }),
		Metrics:      Metrics{Total: bad},
	}
	err = l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metrics label set invalid")
	require.NoError(t, l.Stop(context.Background()))
}

// -----------------------------------------------------------------------------
// Lifecycle skeleton (transplant): start / probe / stop / owner-ctx drain
// -----------------------------------------------------------------------------

func TestLoop_StartStopGraceful(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{RequeueAfter: testtime.D1h}, nil }),
		Interval:     testtime.D1h,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx)) // fast-return probe must unblock
	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
	require.NoError(t, l.Stop(sc)) // idempotent
}

func TestLoop_OwnerCtxCancelDrainsWithoutStop(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{RequeueAfter: testtime.D1h}, nil }),
		Interval:     testtime.D1h,
		Metrics:      m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	require.NoError(t, l.Start(ownerCtx))
	assert.Equal(t, float64(1), p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}),
		"leader must be 1 while the loop runs")
	ownerCancel() // assembly shutdown — no explicit Stop; goleak verifies drain
	// F3: leader must return to 0 on owner-cancel drain even WITHOUT an explicit
	// Stop — the done-watcher is the single reset site, so this path no longer
	// leaves reconcile_leader stuck at 1.
	// gaugeValue is a transient read (not a monotone counter), so no channel
	// signal is producible — polling is required here. Timeout raised from
	// D500ms to EventuallyDefault (3 s) for race-CI headroom.
	testwait.External(t, "leader-reset-on-owner-cancel",
		func() bool {
			return p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}) == 0
		},
		testtime.EventuallyDefault, testtime.D1ms, "leader gauge must reset to 0 when owner ctx drains the loop without Stop")
}

// TestLoop_OwnerCtxCanceledBeforeStart mirrors the transplant source's
// TestSweeperLifecycle_OwnerCtxCancelDuringProbe: the owner ctx is canceled
// BEFORE Start, so awaitProbe must take the deterministic cancel pre-check —
// Start returns nil without reporting "started", and the subsequent Stop is a
// no-op (state was cleared). goleak verifies the spawned workers self-drain.
func TestLoop_OwnerCtxCanceledBeforeStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{RequeueAfter: testtime.D1h}, nil }),
		Interval:     testtime.D1h,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	ownerCancel() // cancel BEFORE Start — awaitProbe's pre-check path must fire
	require.NoError(t, l.Start(ownerCtx))
	require.NoError(t, l.Stop(context.Background())) // no-op: state cleared in awaitProbe
}

func TestLoop_StopTimeoutReturnsDeadline(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	rec := newBlockingReconciler()
	rec.ignoreCtx = true // simulate a reconcile stuck mid-work, not yet checking ctx
	src := make(chan Request)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: testtime.D1h}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "stuck"}
	// blockingReconciler.firstCallSig is closed on the first Reconcile entry —
	// deterministic signal, no polling needed.
	testwait.Deterministic(t, rec.firstCallSig, "reconcile-started")

	sc, cancel := context.WithTimeout(context.Background(), testtime.D1ms)
	defer cancel()
	err := l.Stop(sc)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "Stop must return its ctx deadline when a reconcile is stuck")

	close(rec.release) // unstick so the worker drains and goleak stays clean
}

// -----------------------------------------------------------------------------
// Concurrency bound + same-entity serialization
// -----------------------------------------------------------------------------

func TestLoop_MaxConcurrencyRespected(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	rec := newBlockingReconciler()
	src := make(chan Request)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, MaxConcurrentReconciles: 2, Interval: testtime.D1h}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	for i := 0; i < 5; i++ {
		src <- Request{EntityID: fmt.Sprintf("e%d", i)}
	}
	// currentConcurrent() is a transient peak (not a monotone counter): the
	// value may drop back below 2 before we observe it. A channel signal would
	// require peeking into the scheduler, which is not possible. Keep External
	// but raise the timeout from D500ms to EventuallyDefault (3 s) for CI headroom.
	testwait.External(t, "two-concurrent-reconciles", func() bool { return rec.currentConcurrent() == 2 },
		testtime.EventuallyDefault, testtime.D1ms, "exactly 2 reconciles should run with MaxConcurrentReconciles=2")
	maxC, _ := rec.snapshot()
	assert.LessOrEqual(t, maxC, 2, "concurrency must never exceed the worker bound")

	close(rec.release)
	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
	maxC, _ = rec.snapshot()
	assert.Equal(t, 2, maxC, "two workers should have reached 2-way concurrency")
}

// TestLoop_SameEntityIDSerial verifies same-EntityID serialization under F5
// dirty/processing dedup semantics:
//   - 4 requests for the same entity arrive while MaxConcurrentReconciles=4.
//   - The first request enters reconcile (processing=true); the other three are
//     coalesced into ONE dirty re-run (each records resultSkipped).
//   - At no point does more than 1 reconcile run concurrently for the same entity
//     (maxPerEntity == 1).
//   - After the first reconcile completes, the coalesced dirty re-run is enqueued
//     immediately (delay=0) and reconciled exactly once more; Stop drains it.
//
// F5 semantics change from A3: duplicates are no longer silently dropped — the
// dirty re-run ensures convergence even without a resync. The skip counter still
// fires 3 times (one per duplicate trigger), and maxPerEntity stays 1.
func TestLoop_SameEntityIDSerial(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	rec := newBlockingReconciler()
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, MaxConcurrentReconciles: 4, Interval: testtime.D1h, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	// Register the deterministic signal BEFORE Start so no skip increment is
	// missed between Start and the first Request send.
	skippedThrice := p.signalWhenCounterReaches(
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSkipped)}, 3)
	require.NoError(t, l.Start(ownerCtx))

	for i := 0; i < 4; i++ {
		src <- Request{EntityID: "same"}
	}
	// First reconcile holds the entity (processing=true); the other three are
	// coalesced as dirty (each records resultSkipped). The channel fires
	// deterministically when skip count reaches 3 — no wall-clock polling.
	testwait.Deterministic(t, skippedThrice, "three-duplicates-skipped")
	// Wait for the first Reconcile entry to ensure maxPerEntity is observable.
	testwait.Deterministic(t, rec.firstCallSig, "first-reconcile-entered")
	_, maxPE := rec.snapshot()
	assert.Equal(t, 1, maxPE, "the same EntityID must never reconcile concurrently")

	// Release the blocking reconcile. The dirty re-run is enqueued at delay=0
	// and will be picked up by a worker; Stop drains it.
	// The exactly-once-more guarantee is further verified by asserting total call
	// count == 2 after Stop (one in-flight + one dirty re-run). This complements
	// TestLoop_F5_DirtyDedupCoalescedRerun which also asserts this guarantee.
	secondCall := p.signalWhenCounterReaches(
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSuccess)}, 2)
	close(rec.release)
	testwait.Deterministic(t, secondCall, "dirty-rerun-success")
	assert.EqualValues(t, 2, rec.calls.Load(), "exactly 2 reconcile calls: in-flight + coalesced dirty re-run")
	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

// -----------------------------------------------------------------------------
// Panic isolation + error classification + metrics
// -----------------------------------------------------------------------------

func TestLoop_PanicRecoveredAndOtherEntitiesUnaffected(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	// okSig is closed the first time the non-panicking "ok" entity is reconciled.
	okSig := make(chan struct{})
	var okOnce sync.Once
	rec := funcReconciler(func(_ context.Context, req Request) (Result, error) {
		if req.EntityID == "boom" {
			panic("kaboom")
		}
		okOnce.Do(func() { close(okSig) })
		return Result{RequeueAfter: testtime.D1h}, nil
	})
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	// Register the transient-counter signal before Start so no increment is missed.
	panicTransient := p.signalWhenCounterReaches(
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultTransient)}, 1)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: testtime.D1h, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "boom"}
	src <- Request{EntityID: "ok"}

	// okSig is closed deterministically when the "ok" entity completes its first
	// reconcile — channel signal, no polling.
	testwait.Deterministic(t, okSig, "other-entity-reconciled")
	// panicTransient is closed deterministically when the transient counter hits 1.
	testwait.Deterministic(t, panicTransient, "panic-counted-transient")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

func TestLoop_PermanentNotRequeued_TransientRequeued(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	rec := funcReconciler(func(_ context.Context, req Request) (Result, error) {
		if req.EntityID == "perm" {
			return Result{}, PermanentError(errors.New("revoked"))
		}
		return Result{}, errors.New("transient blip")
	})
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	// Register the deterministic signal before Start so no increment is missed
	// between Start and the first Request send.
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultTransient)}
	permLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultPermanent)}
	transientThrice := p.signalWhenCounterReaches(transLabels, 3)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: shortRequeue, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "perm"}
	src <- Request{EntityID: "trans"}

	// Once the transient entity has been requeued+retried several times, enough
	// intervals have elapsed that the permanent entity would also have requeued
	// if it were going to — assert it stayed at exactly one reconcile.
	// Channel signal from recordingProvider.signalWhenCounterReaches; no polling.
	testwait.Deterministic(t, transientThrice, "transient-requeued-thrice")
	assert.Equal(t, int64(1), p.counterValue(permLabels),
		"a PermanentError entity must reconcile exactly once (dead-lettered, not requeued)")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

func TestLoop_RecordsSuccessMetrics(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	rec := funcReconciler(func(context.Context, Request) (Result, error) {
		return Result{RequeueAfter: testtime.D1h}, nil
	})
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	// Register the deterministic signal before Start so no increment is missed.
	successLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSuccess)}
	successSig := p.signalWhenCounterReaches(successLabels, 1)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: testtime.D1h, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	// Leader is set to 1 at Start (single-process).
	assert.Equal(t, float64(1), p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}))

	src <- Request{EntityID: "e1"}
	// Channel signal from recordingProvider.signalWhenCounterReaches; no polling.
	testwait.Deterministic(t, successSig, "success-recorded")
	assert.GreaterOrEqual(t, p.histogramCount(metricReconcileDuration, kernelmetrics.Labels{labelReconciler: "rc"}), int64(1),
		"reconcile_duration_seconds must be observed")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
	// In-flight returns to 0 and leader to 0 after Stop.
	assert.Equal(t, float64(0), p.gaugeValue(metricReconcileInFlight, kernelmetrics.Labels{labelReconciler: "rc"}))
	assert.Equal(t, float64(0), p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}))
}

// -----------------------------------------------------------------------------
// Review fixes: leader-gauge reset paths (F3/F4) + ReconcilerID validation (F7)
// -----------------------------------------------------------------------------

// TestLoop_OwnerCtxCanceledBeforeStartResetsLeader covers F3: Start
// optimistically sets reconcile_leader=1, then awaitProbe's owner-cancel
// pre-check returns without confirming the loop and makes the subsequent Stop a
// no-op. The gauge MUST be reset to 0 on that path, else it stays stuck at 1.
func TestLoop_OwnerCtxCanceledBeforeStartResetsLeader(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{RequeueAfter: testtime.D1h}, nil }),
		Interval:     testtime.D1h,
		Metrics:      m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	ownerCancel() // cancel BEFORE Start — awaitProbe pre-check fires
	require.NoError(t, l.Start(ownerCtx))
	// The reset is via the done-watcher (single reset site), so it is async to
	// Start returning — poll until the spawned workers drain and reset it.
	// gaugeValue is a transient read (not a monotone counter), so no channel
	// signal is producible. Timeout raised from D500ms to EventuallyDefault (3 s).
	testwait.External(t, "leader-reset-precancel",
		func() bool {
			return p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}) == 0
		},
		testtime.EventuallyDefault, testtime.D1ms, "leader must reset to 0 when owner ctx is canceled before the loop confirms running")
	require.NoError(t, l.Stop(context.Background())) // no-op; state already cleared
}

// TestLoop_StopRetryableAfterTimeout covers F4: a Stop that exhausts its budget
// while a reconcile is stuck must NOT prematurely clear l.cancel/l.done or reset
// the leader gauge. State is retained so a concurrent Start stays a no-op (no
// second pool racing the draining one) and a later Stop drains and resets the
// gauge to 0.
func TestLoop_StopRetryableAfterTimeout(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	rec := newBlockingReconciler()
	rec.ignoreCtx = true // stuck mid-work, ignores ctx until released
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: testtime.D1h, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "stuck"}
	// blockingReconciler.firstCallSig is closed on the first Reconcile entry —
	// deterministic signal, no polling needed.
	testwait.Deterministic(t, rec.firstCallSig, "reconcile-started")

	// First Stop times out (reconcile stuck): returns deadline, leaves state.
	sc1, cancel1 := context.WithTimeout(context.Background(), testtime.D1ms)
	defer cancel1()
	require.ErrorIs(t, l.Stop(sc1), context.DeadlineExceeded)
	assert.Equal(t, float64(1), p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}),
		"a timed-out Stop must leave leader=1 (loop not yet drained)")
	l.mu.Lock()
	retained := l.cancel != nil && l.done != nil
	l.mu.Unlock()
	assert.True(t, retained, "a timed-out Stop must retain l.cancel/l.done so Start stays a no-op and Stop is retryable")

	// Start during draining must be a no-op (does not spawn a second pool).
	require.NoError(t, l.Start(ownerCtx))
	assert.EqualValues(t, 1, rec.calls.Load(), "Start during draining must not spawn a second pool / reconcile")

	// Unstick, then a retried Stop drains and resets the gauge.
	close(rec.release)
	sc2, cancel2 := stopCtx(t)
	defer cancel2()
	require.NoError(t, l.Stop(sc2), "Stop must be retryable after a timeout")
	assert.Equal(t, float64(0), p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}))
	l.mu.Lock()
	cleared := l.cancel == nil && l.done == nil
	l.mu.Unlock()
	assert.True(t, cleared, "a drained Stop must clear l.cancel/l.done")
}

// TestValidateReconcilerID covers F7: the owner-dimension validator that keeps a
// ReconcilerID label-safe and low-cardinality.
func TestValidateReconcilerID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"empty uses sentinel", "", true},
		{"simple", "mdmcell", true},
		{"digits and underscore", "mdm_cell2", true},
		{"sentinel", reconcilerIDSentinel, true},
		{"leading underscore", "_x", true},
		{"max length", strings.Repeat("a", maxReconcilerIDLen), true},
		{"leading digit", "2cell", false},
		{"uppercase", "Cell", false},
		{"dash", "mdm-cell", false},
		{"dot", "mdm.cell", false},
		{"space", "mdm cell", false},
		{"label separator pipe", "a|b", false},
		{"label separator equals", "a=b", false},
		{"too long", strings.Repeat("a", maxReconcilerIDLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReconcilerID(tc.id)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestLoop_BadReconcilerIDFailsStart covers F7 at the Start gate: a malformed
// ReconcilerID fails OnStart (bootstrap rolls back) before any metric record.
func TestLoop_BadReconcilerIDFailsStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	l := &Loop{
		ReconcilerID: "Bad-ID",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{}, nil }),
	}
	err := l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReconcilerID")
	require.NoError(t, l.Stop(context.Background())) // no-op, goleak stays clean
}

// -----------------------------------------------------------------------------
// F5 dirty/processing dedup + F6 shared delaying queue tests
// -----------------------------------------------------------------------------

// TestLoop_F5_DirtyDedupCoalescedRerun verifies the F5 dirty/processing dedup
// guarantee: N duplicate triggers arriving while one reconcile is in flight are
// coalesced into exactly ONE re-run after the in-flight reconcile completes.
//
// Setup: MaxConcurrentReconciles=4, 1 slow entity, 4 duplicate triggers.
// Expected: reconcile count == 2 (one in-flight + one dirty re-run), maxPerEntity==1.
func TestLoop_F5_DirtyDedupCoalescedRerun(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const entity = "coalesce-me"

	// secondStarted is closed when the second (dirty re-run) reconcile begins.
	secondStarted := make(chan struct{})
	var secondOnce sync.Once

	var callCount atomic.Int64
	release := make(chan struct{})

	rec := funcReconciler(func(ctx context.Context, req Request) (Result, error) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: block until released.
			select {
			case <-release:
			case <-ctx.Done():
			}
		} else {
			// Second call: signal immediately.
			secondOnce.Do(func() { close(secondStarted) })
		}
		return Result{RequeueAfter: testtime.D1h}, nil
	})

	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	// shortRequeue keeps periodic resync from re-triggering during the test.
	l := &Loop{
		ReconcilerID:            "rc",
		Reconciler:              rec,
		Source:                  src,
		MaxConcurrentReconciles: 4,
		Interval:                testtime.D1h,
		Metrics:                 m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()

	// Register skip signal before Start so no increment is missed.
	skippedThrice := p.signalWhenCounterReaches(
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSkipped)}, 3)

	require.NoError(t, l.Start(ownerCtx))

	// Send 4 requests: first enters reconcile, next 3 are coalesced as dirty.
	for i := 0; i < 4; i++ {
		src <- Request{EntityID: entity}
	}
	// Wait until 3 skips are recorded (3 duplicates coalesced).
	testwait.Deterministic(t, skippedThrice, "three-skips-coalesced")

	// Release the first reconcile. This triggers the dirty re-run at delay=0.
	close(release)

	// The dirty re-run must start exactly once.
	testwait.Deterministic(t, secondStarted, "dirty-rerun-started")
	assert.EqualValues(t, 2, callCount.Load(), "exactly 2 reconcile calls: in-flight + coalesced dirty re-run")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

// TestLoop_F5_LostWakeupStress stresses the F5 dirty/processing completion
// window. Many goroutines call process() concurrently for ONE entity (so all
// but one in-flight call coalesce as dirty); runtime.Gosched widens the
// interleaving so the {clear-dirty, delete-processing} critical section is hit
// across runs. Run under -race; `-count=N` amplifies window coverage.
//
// Liveness invariant (the lost-wakeup detector): once every process() call has
// returned, no entity may remain marked dirty or processing. A lost wakeup — a
// trigger that set dirty in the window after completion read+cleared dirty but
// before it deleted the processing marker — would orphan the dirty entry with
// no re-run ever scheduled, leaving l.dirty non-empty here. The atomicity that
// prevents it (entityMu held across both the dirty read+clear and the
// processing delete, loop.go process()) is what this test exercises; -race
// additionally proves no unlocked access to the maps.
//
// This is the deterministic-under-correct-code complement to the precise-window
// approach: GoCell does not inject a test-only synchronization hook into the
// production critical section (that would put test scaffolding on the hot path
// for marginal gain over the mutex-by-construction guarantee), so a probabilistic
// stress sweep is the cleanest available coverage.
func TestLoop_F5_LostWakeupStress(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		entity   = "stress-entity"
		triggers = 256
	)

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		runtime.Gosched()
		return Result{RequeueAfter: testtime.D1h}, nil
	})
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   rec,
		Source:       make(chan Request),
		Interval:     testtime.D1h,
		Metrics:      m,
	}

	// White-box: initialize the F5 maps the way Start does, then drive process()
	// directly (no worker pool) to stress its critical sections in isolation.
	l.processing = make(map[string]bool)
	l.dirty = make(map[string]Request)
	backoff := newEntityBackoff(defaultBackoffBase, defaultBackoffMax)
	runCtx := context.Background()

	// Discard requeue / dirty re-run / cancel items so the send funnels never
	// block; the invariant under test is the dirty map state, not re-run execution.
	addCh := make(chan waitingItem, triggers*4)
	cancelCh := make(chan string, triggers*4)
	drainDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-addCh:
			case <-cancelCh:
			case <-drainDone:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < triggers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.Gosched()
			l.process(runCtx, Request{EntityID: entity}, addCh, cancelCh, backoff)
		}()
	}
	wg.Wait()
	close(drainDone)

	l.entityMu.Lock()
	defer l.entityMu.Unlock()
	assert.Empty(t, l.dirty, "no orphaned dirty entry (lost-wakeup) after quiescence")
	assert.Empty(t, l.processing, "no entity left marked processing after quiescence")
}

// -----------------------------------------------------------------------------
// F5: delaying-queue heap ordering / earlier-readyAt preemption (direct tests)
// -----------------------------------------------------------------------------

// TestWaitingHeap_OrdersByReadyAt proves the min-heap pops entries in ascending
// readyAt order regardless of insertion order (Less is by readyAt).
func TestWaitingHeap_OrdersByReadyAt(t *testing.T) {
	t.Parallel()
	base := time.Unix(0, 0)
	var h waitingHeap
	heap.Init(&h)
	heap.Push(&h, &waitingItem{req: Request{EntityID: "c"}, readyAt: base.Add(testtime.D30ms)})
	heap.Push(&h, &waitingItem{req: Request{EntityID: "a"}, readyAt: base.Add(testtime.D1ms)})
	heap.Push(&h, &waitingItem{req: Request{EntityID: "b"}, readyAt: base.Add(testtime.D10ms)})

	var order []string
	for h.Len() > 0 {
		order = append(order, heap.Pop(&h).(*waitingItem).req.EntityID)
	}
	assert.Equal(t, []string{"a", "b", "c"}, order, "heap must pop earliest readyAt first")
}

// TestAddOrMergeWaiting_EarlierReadyAtWins proves the per-entity merge (F1): a
// repeat requeue for an already-waiting entity keeps a SINGLE heap entry and
// adopts the earlier readyAt (a sooner dirty re-run preempts a later
// interval/backoff entry); a later readyAt never supersedes a sooner one.
func TestAddOrMergeWaiting_EarlierReadyAtWins(t *testing.T) {
	t.Parallel()
	base := time.Unix(0, 0)
	var h waitingHeap
	heap.Init(&h)
	pending := map[string]*waitingItem{}

	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "x"}, readyAt: base.Add(testtime.D30ms)})
	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "x"}, readyAt: base.Add(testtime.D5ms)})
	require.Equal(t, 1, h.Len(), "same entity must occupy exactly one heap entry")
	assert.Equal(t, base.Add(testtime.D5ms), h[0].readyAt, "earlier readyAt must win the merge")

	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "x"}, readyAt: base.Add(testtime.D50ms)})
	require.Equal(t, 1, h.Len(), "merge must not add a second entry for the same entity")
	assert.Equal(t, base.Add(testtime.D5ms), h[0].readyAt, "a later readyAt must not supersede the earlier one")
}

// TestDrainReadyItems_PopsOnlyReadyAndClearsPending proves drainReadyItems moves
// only entries whose readyAt has arrived into the queue, leaves future entries
// on the heap, and keeps the pending index in sync (drained entity removed,
// not-yet-ready entity retained).
func TestDrainReadyItems_PopsOnlyReadyAndClearsPending(t *testing.T) {
	t.Parallel()
	base := time.Unix(0, 0)
	var h waitingHeap
	heap.Init(&h)
	pending := map[string]*waitingItem{}
	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "ready"}, readyAt: base})
	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "future"}, readyAt: base.Add(testtime.D1h)})

	queue := make(chan Request, 4)
	require.True(t, drainReadyItems(context.Background(), &h, pending, base, queue))

	require.Len(t, queue, 1, "only the ready entity is drained")
	assert.Equal(t, "ready", (<-queue).EntityID)
	_, hasReady := pending["ready"]
	_, hasFuture := pending["future"]
	assert.False(t, hasReady, "drained entity must be removed from pending")
	assert.True(t, hasFuture, "not-yet-ready entity must stay in pending")
	assert.Equal(t, 1, h.Len(), "the future item remains on the heap")
}

// TestCancelPending_RemovesFromHeapAndPending proves the cancel primitive: it
// removes exactly the named entity from both the heap and the pending index,
// leaves other entities untouched, and is a no-op for an unknown entity.
func TestCancelPending_RemovesFromHeapAndPending(t *testing.T) {
	t.Parallel()
	base := time.Unix(0, 0)
	var h waitingHeap
	heap.Init(&h)
	pending := map[string]*waitingItem{}
	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "x"}, readyAt: base.Add(testtime.D1h)})
	addOrMergeWaiting(&h, pending, waitingItem{req: Request{EntityID: "y"}, readyAt: base.Add(testtime.D1h)})

	cancelPending(&h, pending, "x")
	require.Equal(t, 1, h.Len(), "canceled entity must be removed from the heap")
	_, hasX := pending["x"]
	_, hasY := pending["y"]
	assert.False(t, hasX, "canceled entity must be removed from pending")
	assert.True(t, hasY, "other entities must be untouched")
	assert.Equal(t, "y", h[0].req.EntityID)

	cancelPending(&h, pending, "unknown") // no-op
	assert.Equal(t, 1, h.Len(), "canceling an unknown entity must be a no-op")
}

// TestDispatchResult_PermanentCancelsPendingNotRequeue proves F1-(c): on a
// permanent result, dispatchResult sends the entity on cancelCh (to evict any
// stale pre-existing requeue) and does NOT enqueue a new requeue. This is the
// "stale pending then permanent" path the prior code left uncovered: an earlier
// success/transient could have enqueued a long requeue that, without this
// cancel, would re-reconcile a dead-lettered entity when it fired.
func TestDispatchResult_PermanentCancelsPendingNotRequeue(t *testing.T) {
	t.Parallel()
	l := &Loop{ReconcilerID: "rc"}
	addCh := make(chan waitingItem, 1)
	cancelCh := make(chan string, 1)
	backoff := newEntityBackoff(defaultBackoffBase, defaultBackoffMax)

	l.dispatchResult(context.Background(), Request{EntityID: "dead"}, Result{},
		PermanentError(errors.New("boom")), resultPermanent, addCh, cancelCh, backoff)

	select {
	case id := <-cancelCh:
		assert.Equal(t, "dead", id, "permanent must cancel the entity's pending requeue")
	default:
		t.Fatal("permanent result must send the entity on cancelCh")
	}
	assert.Empty(t, addCh, "permanent must NOT enqueue a new requeue")
}

// TestDispatchResult_SuccessEnqueuesNotCancel is the over-fire guard: a success
// result enqueues a requeue (addCh) and does NOT cancel.
func TestDispatchResult_SuccessEnqueuesNotCancel(t *testing.T) {
	t.Parallel()
	l := &Loop{ReconcilerID: "rc", Interval: testtime.D1h}
	addCh := make(chan waitingItem, 1)
	cancelCh := make(chan string, 1)
	backoff := newEntityBackoff(defaultBackoffBase, defaultBackoffMax)

	l.dispatchResult(context.Background(), Request{EntityID: "ok"}, Result{RequeueAfter: testtime.D1h},
		nil, resultSuccess, addCh, cancelCh, backoff)

	assert.Len(t, addCh, 1, "success must enqueue a requeue")
	assert.Empty(t, cancelCh, "success must NOT cancel")
}

// TestLoop_BaseDelayExceedsMaxDelayFailsStart proves F6: an inverted backoff
// window (BaseDelay > MaxDelay, both explicitly set) is rejected at Start.
func TestLoop_BaseDelayExceedsMaxDelayFailsStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{}, nil }),
		BaseDelay:    testtime.D1h,
		MaxDelay:     shortRequeue,
	}
	err := l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDelay")
	assert.Contains(t, err.Error(), "MaxDelay")
	require.NoError(t, l.Stop(context.Background()))
}

// TestLoop_TransientExponentialBackoff is a count-based integration check: it
// asserts the loop retries a transient entity multiple times (confirming the
// delaying queue feeds retries back). No timing assertions are made here — the
// exponential/reset timing property is unit-covered by backoff_test.go.
func TestLoop_TransientExponentialBackoff(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultTransient)}
	// Signal after 3 transient retries to confirm retrying actually happens.
	transientThrice := p.signalWhenCounterReaches(transLabels, 3)

	src := make(chan Request)
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler: funcReconciler(func(_ context.Context, _ Request) (Result, error) {
			return Result{}, fmt.Errorf("transient blip")
		}),
		Source:    src,
		Interval:  testtime.D1h,
		BaseDelay: testtime.D1ms, // shrink base for test speed
		MaxDelay:  shortRequeue,  // cap so test completes quickly
		Metrics:   m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "retry-me"}

	// Wait for 3 transient retries: confirms exponential backoff retries the entity
	// repeatedly via the shared delaying queue.
	testwait.Deterministic(t, transientThrice, "three-transient-retries-via-backoff")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

// TestLoop_SuccessForgetsBackoff is a count-based integration check: it
// confirms that after transient→success→transient the entity retries again
// (i.e. the loop does re-schedule after a success clears the backoff). No
// timing assertions are made here — the reset timing property is unit-covered
// by TestBackoff_ResetOnSuccess in backoff_test.go.
func TestLoop_SuccessForgetsBackoff(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	// Phase control: first call → transient, second → success, subsequent → transient.
	var callCount atomic.Int64
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		n := callCount.Add(1)
		switch n {
		case 1:
			return Result{}, fmt.Errorf("first transient")
		case 2:
			return Result{RequeueAfter: testtime.D1ms}, nil // success: requeue quickly for next call
		default:
			return Result{}, fmt.Errorf("post-success transient")
		}
	})

	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultTransient)}
	succLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSuccess)}
	// Wait for 2 transients and 1 success to confirm the full flow.
	twoTransients := p.signalWhenCounterReaches(transLabels, 2)
	oneSuccess := p.signalWhenCounterReaches(succLabels, 1)

	src := make(chan Request)
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   rec,
		Source:       src,
		Interval:     testtime.D1h,
		BaseDelay:    testtime.D1ms, // tiny base so test completes quickly
		MaxDelay:     shortRequeue,
		Metrics:      m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "forget-me"}

	// Phase 1: first transient fires. Then success fires (requeue at 1ms). Then
	// second transient fires — this is the "forgot backoff" call that would be very
	// slow if backoff wasn't reset (accumulated would be ≥20ms, but reset means 1ms).
	testwait.Deterministic(t, oneSuccess, "success-recorded")
	testwait.Deterministic(t, twoTransients, "two-transients-confirm-forgot-backoff")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

// TestLoop_SharedWaitingLoopNoLeak verifies that the F6 shared delaying queue
// (waitingLoop goroutine) exits cleanly when Stop is called, even when there are
// many pending requeue items. goleak.VerifyNone asserts that no goroutines leak.
//
// Design confirmation: the waitingLoop goroutine is tracked by the WaitGroup and
// exits on runCtx.Done(), so Stop waits for it. This test seeds many requeues and
// verifies zero goroutine leak.
func TestLoop_SharedWaitingLoopNoLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const nEntities = 20

	// Reconciler always returns transient so items keep being requeued into the
	// delaying queue with a long BaseDelay (many pending items at Stop time).
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultTransient)}
	// Wait until every entity has been seen at least once (nEntities transients).
	allSeenOnce := p.signalWhenCounterReaches(transLabels, nEntities)

	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, fmt.Errorf("keep retrying")
	})

	src := make(chan Request, nEntities)
	for i := 0; i < nEntities; i++ {
		src <- Request{EntityID: fmt.Sprintf("e%d", i)}
	}

	l := &Loop{
		ReconcilerID:            "rc",
		Reconciler:              rec,
		Source:                  src,
		MaxConcurrentReconciles: 4,
		Interval:                testtime.D1h,
		BaseDelay:               testtime.D1h, // long delay → many items pending in heap at Stop
		MaxDelay:                testtime.D1h,
		Metrics:                 m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	// Wait until all entities have been reconciled once (nEntities pending requeues
	// are now in the delaying heap with BaseDelay=1h).
	testwait.Deterministic(t, allSeenOnce, "all-entities-seen-once")

	// Stop must drain the waitingLoop goroutine without leaking. goleak confirms.
	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
	// goleak.VerifyNone at defer confirms no leaked goroutines.
}

// TestLoop_StopCleansEntityMaps verifies that the processing and dirty maps are
// empty (no residual state) after Stop, even when entities were in-flight or
// dirty at shutdown. This guards against state-leak across Loop restarts (S2).
//
// Design: we send one entity into a blocking reconcile, then send duplicates to
// ensure dirty state is set. We then release and Stop; by the time Stop returns
// (watchDrain has run), all goroutines have exited and entity maps are empty.
func TestLoop_StopCleansEntityMaps(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const entity = "in-flight-entity"

	rec := newBlockingReconciler()
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	// Register skip signal before Start so no increment is missed.
	skippedOnce := p.signalWhenCounterReaches(
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSkipped)}, 1)

	l := &Loop{
		ReconcilerID:            "rc",
		Reconciler:              rec,
		Source:                  src,
		MaxConcurrentReconciles: 4,
		Interval:                testtime.D1h,
		BaseDelay:               testtime.D1h,
		MaxDelay:                testtime.D1h,
		Metrics:                 m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	// First request enters reconcile (processing=true); second is coalesced as dirty.
	src <- Request{EntityID: entity}
	testwait.Deterministic(t, rec.firstCallSig, "first-reconcile-entered")
	src <- Request{EntityID: entity} // coalesced as dirty
	testwait.Deterministic(t, skippedOnce, "dirty-set")

	// Release the in-flight reconcile, then Stop.
	close(rec.release)
	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))

	// After Stop: both maps must be empty (no residual state).
	l.entityMu.Lock()
	processingLen := len(l.processing)
	dirtyLen := len(l.dirty)
	l.entityMu.Unlock()
	assert.Equal(t, 0, processingLen, "processing map must be empty after Stop")
	assert.Equal(t, 0, dirtyLen, "dirty map must be empty after Stop")
}

// TestLoop_ResyncSentinelIsolation verifies that the empty-EntityID ("") resync
// sentinel participates in the dirty/processing and backoff maps like any other
// entity key — isolated from real entity keys and not subject to special-casing.
//
// This covers the resync-all pattern: a Trigger produces Request{EntityID: ""}
// to re-observe all entities. The empty key should not collide with real keys.
func TestLoop_ResyncSentinelIsolation(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	sentinelLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSuccess)}
	successTwice := p.signalWhenCounterReaches(sentinelLabels, 2)

	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{RequeueAfter: testtime.D1h}, nil
	})

	src := make(chan Request, 2)
	src <- Request{EntityID: ""}     // resync sentinel
	src <- Request{EntityID: "real"} // real entity

	l := &Loop{
		ReconcilerID:            "rc",
		Reconciler:              rec,
		Source:                  src,
		MaxConcurrentReconciles: 2,
		Interval:                testtime.D1h,
		BaseDelay:               testtime.D1h, // no spurious retries
		MaxDelay:                testtime.D1h,
		Metrics:                 m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	// Both the sentinel ("") and the real entity must reconcile successfully.
	testwait.Deterministic(t, successTwice, "both-sentinel-and-real-reconciled")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

// TestLoop_SuccessRequeueAfterPositive verifies that a Reconciler returning
// Result{RequeueAfter: d} (d > 0) causes the entity to be re-enqueued via the
// shared delaying queue at delay d — specifically that the
// `delay = res.normalizedRequeueAfter()` branch in dispatchResult is exercised
// and the entity is reconciled a second time.
//
// Design: the reconciler returns a small positive RequeueAfter on the first call
// and a long RequeueAfter on the second (so no spurious third call during the
// test). We wait for two successes using signalWhenCounterReaches — no wall-clock
// sleeps.
func TestLoop_SuccessRequeueAfterPositive(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	successLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: string(resultSuccess)}
	// Register BEFORE Start so no success count is missed.
	successTwice := p.signalWhenCounterReaches(successLabels, 2)

	var callCount atomic.Int64
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: small positive RequeueAfter — drives the delaying-queue path.
			return Result{RequeueAfter: testtime.D1ms}, nil
		}
		// Second call: requeue far out so the test ends promptly.
		return Result{RequeueAfter: testtime.D1h}, nil
	})

	src := make(chan Request, 1)
	src <- Request{EntityID: "requeue-after-entity"}

	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   rec,
		Source:       src,
		Interval:     testtime.D1h,
		Metrics:      m,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	// Wait for 2 successes: first call + re-enqueued second call via delaying queue.
	// This deterministically confirms the RequeueAfter > 0 path feeds the entity
	// back through the shared delaying queue.
	testwait.Deterministic(t, successTwice, "two-successes-via-requeue-after")
	assert.EqualValues(t, 2, callCount.Load(),
		"entity must have been reconciled twice: once initial, once via RequeueAfter delaying queue")

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}
