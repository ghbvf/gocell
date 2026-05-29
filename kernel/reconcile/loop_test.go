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
}

func newBlockingReconciler() *blockingReconciler {
	return &blockingReconciler{
		release:   make(chan struct{}),
		perEntity: map[string]int{},
		result:    Result{RequeueAfter: testtime.D1h}, // requeue far out — no re-fire mid-test
	}
}

func (r *blockingReconciler) Reconcile(ctx context.Context, req Request) (Result, error) {
	r.calls.Add(1)
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
	testwait.External(t, "leader-reset-on-owner-cancel",
		func() bool {
			return p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}) == 0
		},
		testtime.D500ms, testtime.D1ms, "leader gauge must reset to 0 when owner ctx drains the loop without Stop")
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
	testwait.External(t, "reconcile-started", func() bool { return rec.calls.Load() >= 1 },
		testtime.D500ms, testtime.D1ms, "a reconcile must be running before Stop")

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
	testwait.External(t, "two-concurrent-reconciles", func() bool { return rec.currentConcurrent() == 2 },
		testtime.D500ms, testtime.D1ms, "exactly 2 reconciles should run with MaxConcurrentReconciles=2")
	maxC, _ := rec.snapshot()
	assert.LessOrEqual(t, maxC, 2, "concurrency must never exceed the worker bound")

	close(rec.release)
	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
	maxC, _ = rec.snapshot()
	assert.Equal(t, 2, maxC, "two workers should have reached 2-way concurrency")
}

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
	require.NoError(t, l.Start(ownerCtx))

	for i := 0; i < 4; i++ {
		src <- Request{EntityID: "same"}
	}
	// First reconcile holds the entity; the other three are dequeued and dropped
	// as "skipped" (level-triggered serialization), never running concurrently.
	testwait.External(t, "three-duplicates-skipped",
		func() bool {
			return p.counterValue(kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSkipped}) >= 3
		},
		testtime.D500ms, testtime.D1ms, "duplicate same-entity requests must be skipped while one is in flight")
	_, maxPE := rec.snapshot()
	assert.Equal(t, 1, maxPE, "the same EntityID must never reconcile concurrently")

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
	var okCalls atomic.Int64
	rec := funcReconciler(func(_ context.Context, req Request) (Result, error) {
		if req.EntityID == "boom" {
			panic("kaboom")
		}
		okCalls.Add(1)
		return Result{RequeueAfter: testtime.D1h}, nil
	})
	src := make(chan Request)
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: testtime.D1h, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "boom"}
	src <- Request{EntityID: "ok"}

	testwait.External(t, "other-entity-reconciled", func() bool { return okCalls.Load() >= 1 },
		testtime.D500ms, testtime.D1ms, "a panicking entity must not stop other entities from reconciling")
	testwait.External(t, "panic-counted-transient",
		func() bool {
			return p.counterValue(kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}) >= 1
		},
		testtime.D500ms, testtime.D1ms, "a recovered panic must be classified transient")

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
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: shortRequeue, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "perm"}
	src <- Request{EntityID: "trans"}

	// Once the transient entity has been requeued+retried several times, enough
	// intervals have elapsed that the permanent entity would also have requeued
	// if it were going to — assert it stayed at exactly one reconcile.
	transLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultTransient}
	permLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultPermanent}
	testwait.External(t, "transient-requeued-thrice",
		func() bool { return p.counterValue(transLabels) >= 3 },
		testtime.D500ms, testtime.D1ms, "transient errors must be requeued and retried")
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
	l := &Loop{ReconcilerID: "rc", Reconciler: rec, Source: src, Interval: testtime.D1h, Metrics: m}
	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	// Leader is set to 1 at Start (single-process).
	assert.Equal(t, float64(1), p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}))

	src <- Request{EntityID: "e1"}
	successLabels := kernelmetrics.Labels{labelReconciler: "rc", labelResult: resultSuccess}
	testwait.External(t, "success-recorded",
		func() bool { return p.counterValue(successLabels) >= 1 },
		testtime.D500ms, testtime.D1ms, "a successful reconcile must increment reconcile_total{result=success}")
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
	testwait.External(t, "leader-reset-precancel",
		func() bool {
			return p.gaugeValue(metricReconcileLeader, kernelmetrics.Labels{labelReconciler: "rc"}) == 0
		},
		testtime.D500ms, testtime.D1ms, "leader must reset to 0 when owner ctx is canceled before the loop confirms running")
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
	testwait.External(t, "reconcile-started", func() bool { return rec.calls.Load() >= 1 },
		testtime.D500ms, testtime.D1ms, "a reconcile must be running before Stop")

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
