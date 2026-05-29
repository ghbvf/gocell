package reconcile

import (
	"context"
	"errors"
	"fmt"
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
	l := &Loop{
		ReconcilerID: "rc",
		Reconciler:   funcReconciler(func(context.Context, Request) (Result, error) { return Result{RequeueAfter: testtime.D1h}, nil }),
		Interval:     testtime.D1h,
	}
	ownerCtx, ownerCancel := startCtxs(t)
	require.NoError(t, l.Start(ownerCtx))
	ownerCancel() // assembly shutdown — no explicit Stop; goleak verifies drain
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
