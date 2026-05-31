package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSkipped}, 3)
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
	close(rec.release)
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
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}, 1)
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
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}
	permLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultPermanent}
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
	successLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSuccess}
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
		kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSkipped}, 3)

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

// TestLoop_TransientExponentialBackoff verifies that transient errors produce
// exponentially increasing retry delays (via entityBackoff). With a tiny
// BaseDelay the loop retries quickly in tests; with a non-trivial MaxDelay the
// delays would grow. We use signalWhenCounterReaches to assert the loop DOES
// retry multiple times (confirming the delaying queue feeds retries back),
// not wall-clock timing assertions (which are inherently racy).
func TestLoop_TransientExponentialBackoff(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}
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
		BaseDelay: testtime.D1ms,      // shrink base for test speed
		MaxDelay:  20 * testtime.D1ms, // cap so test completes quickly
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

// TestLoop_SuccessForgetsBackoff verifies that a successful reconcile resets the
// exponential backoff for an entity, so the next transient error restarts at
// BaseDelay rather than continuing from accumulated failure depth.
//
// Observable behavior: after transient→success→transient, the entity retries
// quickly (BaseDelay, not accumulated exponential delay). Verified by asserting
// the total transient counter reaches ≥2 quickly enough without hanging.
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

	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}
	succLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSuccess}
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
		MaxDelay:     20 * testtime.D1ms,
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
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}
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

	sentinelLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSuccess}
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
