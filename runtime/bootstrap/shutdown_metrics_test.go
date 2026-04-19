package bootstrap

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// fakeMetricsProvider — records all metric registrations and observations
// ---------------------------------------------------------------------------

// fakeCounterVec records every Add/Inc call along with the label set.
type fakeCounterVec struct {
	mu      sync.Mutex
	labels  []string
	records []fakeCounterRecord
}

type fakeCounterRecord struct {
	labels kernelmetrics.Labels
	delta  float64
}

func (v *fakeCounterVec) Registered() bool { return true }
func (v *fakeCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &fakeCounter{vec: v, labels: l}
}
func (v *fakeCounterVec) totalForLabel(key, value string) float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	var sum float64
	for _, r := range v.records {
		if r.labels[key] == value {
			sum += r.delta
		}
	}
	return sum
}

type fakeCounter struct {
	vec    *fakeCounterVec
	labels kernelmetrics.Labels
}

func (c *fakeCounter) Inc() { c.Add(1) }
func (c *fakeCounter) Add(delta float64) {
	c.vec.mu.Lock()
	defer c.vec.mu.Unlock()
	c.vec.records = append(c.vec.records, fakeCounterRecord{labels: c.labels, delta: delta})
}

// fakeHistogramVec records every Observe call along with the label set.
type fakeHistogramVec struct {
	mu      sync.Mutex
	labels  []string
	records []fakeHistogramRecord
}

type fakeHistogramRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

func (v *fakeHistogramVec) Registered() bool { return true }
func (v *fakeHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &fakeHistogram{vec: v, labels: l}
}
func (v *fakeHistogramVec) observationsForLabel(key, value string) []float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []float64
	for _, r := range v.records {
		if r.labels[key] == value {
			out = append(out, r.value)
		}
	}
	return out
}

type fakeHistogram struct {
	vec    *fakeHistogramVec
	labels kernelmetrics.Labels
}

func (h *fakeHistogram) Observe(value float64) {
	h.vec.mu.Lock()
	defer h.vec.mu.Unlock()
	h.vec.records = append(h.vec.records, fakeHistogramRecord{labels: h.labels, value: value})
}

// fakeMetricsProvider hands out named fake vecs so tests can inspect them.
type fakeMetricsProvider struct {
	mu         sync.Mutex
	counters   map[string]*fakeCounterVec
	histograms map[string]*fakeHistogramVec
}

func newFakeMetricsProvider() *fakeMetricsProvider {
	return &fakeMetricsProvider{
		counters:   make(map[string]*fakeCounterVec),
		histograms: make(map[string]*fakeHistogramVec),
	}
}

func (p *fakeMetricsProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := &fakeCounterVec{labels: append([]string(nil), opts.LabelNames...)}
	p.counters[opts.Name] = v
	return v, nil
}

func (p *fakeMetricsProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := &fakeHistogramVec{labels: append([]string(nil), opts.LabelNames...)}
	p.histograms[opts.Name] = v
	return v, nil
}

func (p *fakeMetricsProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

var _ kernelmetrics.Provider = (*fakeMetricsProvider)(nil)

func (p *fakeMetricsProvider) counter(name string) *fakeCounterVec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counters[name]
}

func (p *fakeMetricsProvider) histogram(name string) *fakeHistogramVec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.histograms[name]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// runWithCancelAndListener starts Bootstrap.Run in a goroutine, waits for the
// HTTP server to become healthy, cancels ctx, then waits for Run to return.
func runWithCancelAndListener(t *testing.T, b *Bootstrap, ln net.Listener, runTimeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- b.Run(ctx) }()

	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get("http://" + ln.Addr().String() + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, 3*time.Second, 20*time.Millisecond, "HTTP server did not become healthy")

	cancel()

	select {
	case err := <-errCh:
		return err
	case <-time.After(runTimeout):
		t.Fatal("bootstrap.Run did not return within timeout after cancel")
		return nil
	}
}

// ---------------------------------------------------------------------------
// Test 1: phase counter transitions
// ---------------------------------------------------------------------------

// TestShutdownMetrics_PhaseCounterTransitions verifies that all three shutdown
// phase labels (readiness_flip, lifo_teardown, closed) are recorded in the
// phase-entry counter and that each is incremented exactly once.
func TestShutdownMetrics_PhaseCounterTransitions(t *testing.T) {
	p := newFakeMetricsProvider()
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "sm-phase", DurabilityMode: cell.DurabilityDemo})
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(3*time.Second),
		WithMetricsProvider(p),
	)

	require.NoError(t, runWithCancelAndListener(t, b, ln, 5*time.Second))

	phaseVec := p.counter(shutdownPhaseCounterName)
	require.NotNil(t, phaseVec, "phase counter %q must be registered", shutdownPhaseCounterName)

	for _, label := range []string{
		shutdownPhaseReadinessFlip,
		shutdownPhaseLIFOTeardown,
		shutdownPhaseClosed,
	} {
		total := phaseVec.totalForLabel("phase", label)
		assert.Equalf(t, float64(1), total,
			"phase %q counter must be incremented exactly once, got %v", label, total)
	}
}

// ---------------------------------------------------------------------------
// Test 2: duration histogram observations
// ---------------------------------------------------------------------------

// TestShutdownMetrics_DurationRecorded verifies that the phase-duration
// histogram receives exactly one observation each for readiness_flip,
// lifo_teardown, and total, each with a non-negative value.
func TestShutdownMetrics_DurationRecorded(t *testing.T) {
	p := newFakeMetricsProvider()
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "sm-dur", DurabilityMode: cell.DurabilityDemo})
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(3*time.Second),
		WithMetricsProvider(p),
	)

	require.NoError(t, runWithCancelAndListener(t, b, ln, 5*time.Second))

	durVec := p.histogram(shutdownPhaseDurationName)
	require.NotNil(t, durVec, "duration histogram %q must be registered", shutdownPhaseDurationName)

	for _, label := range []string{
		shutdownPhaseReadinessFlip,
		shutdownPhaseLIFOTeardown,
		"total",
	} {
		obs := durVec.observationsForLabel("phase", label)
		require.Lenf(t, obs, 1, "expected exactly one duration observation for phase %q", label)
		assert.GreaterOrEqualf(t, obs[0], float64(0),
			"duration for phase %q must be >= 0, got %v", label, obs[0])
	}
}

// ---------------------------------------------------------------------------
// Test 3: outcome counter — success path
// ---------------------------------------------------------------------------

// TestShutdownMetrics_TimeoutOutcome_Success verifies that a clean shutdown
// increments the outcome counter with outcome="success" exactly once.
func TestShutdownMetrics_TimeoutOutcome_Success(t *testing.T) {
	p := newFakeMetricsProvider()
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "sm-ok", DurabilityMode: cell.DurabilityDemo})
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(3*time.Second),
		WithMetricsProvider(p),
	)

	require.NoError(t, runWithCancelAndListener(t, b, ln, 5*time.Second))

	outcomeVec := p.counter(shutdownTotalCounterName)
	require.NotNil(t, outcomeVec, "outcome counter %q must be registered", shutdownTotalCounterName)

	assert.Equal(t, float64(1), outcomeVec.totalForLabel("outcome", "success"),
		"outcome=success must be incremented exactly once on clean shutdown")
	assert.Equal(t, float64(0), outcomeVec.totalForLabel("outcome", "timeout"),
		"outcome=timeout must not be incremented on clean shutdown")
}

// ---------------------------------------------------------------------------
// Test 4: outcome counter — timeout path
// ---------------------------------------------------------------------------

// slowWorker is a background worker whose Stop method blocks until either its
// context is cancelled (ctx.Done) or it is explicitly released. Unlike a cell
// that ignores ctx (which would hang phase10LIFOTeardown indefinitely), this
// worker honours the shutdown context so phase10 can return after the deadline
// and detect DeadlineExceeded.
type slowWorker struct {
	release chan struct{}
}

func newSlowWorker() *slowWorker {
	return &slowWorker{release: make(chan struct{})}
}

func (w *slowWorker) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Stop blocks until ctx is cancelled (timeout path) or release is closed
// (test cleanup path). It returns ctx.Err() on cancellation so the caller
// knows the context expired.
func (w *slowWorker) Stop(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
		return nil
	}
}

// TestShutdownMetrics_TimeoutOutcome_Timeout verifies that when the shutdown
// context expires during teardown, the outcome counter records outcome="timeout".
// Uses a context-aware worker whose Stop respects shutCtx so phase10 can
// proceed after the deadline and record the outcome.
func TestShutdownMetrics_TimeoutOutcome_Timeout(t *testing.T) {
	p := newFakeMetricsProvider()
	ln := newLocalListener(t)
	sw := newSlowWorker()

	asm := assembly.New(assembly.Config{ID: "timeout-test", DurabilityMode: cell.DurabilityDemo})

	// Short timeout so the test completes fast; the slowWorker's Stop will
	// block just long enough for shutCtx to expire.
	const shutdownTimeout = 100 * time.Millisecond
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(shutdownTimeout),
		WithMetricsProvider(p),
		WithWorkers(sw),
	)

	ctx, cancel := context.WithCancel(context.Background())
	// t.Cleanup order: cancel first, then close release (so Stop returns).
	t.Cleanup(func() { close(sw.release) })
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- b.Run(ctx) }()

	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get("http://" + ln.Addr().String() + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, 3*time.Second, 20*time.Millisecond, "HTTP server did not become healthy")

	cancel()

	// Run must return (with a teardown/context error) within 2s.
	// The slowWorker.Stop returns after shutdownTimeout (100ms) via ctx.Done.
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap.Run did not return after shutdown timeout")
	}

	outcomeVec := p.counter(shutdownTotalCounterName)
	require.NotNil(t, outcomeVec, "outcome counter must be registered")
	assert.Equal(t, float64(1), outcomeVec.totalForLabel("outcome", "timeout"),
		"outcome=timeout must be incremented when shutdown context expires")
	assert.Equal(t, float64(0), outcomeVec.totalForLabel("outcome", "success"),
		"outcome=success must not be incremented on timeout")
}

// ---------------------------------------------------------------------------
// Test 4b: outcome counter — teardown_error / signal_error paths (F3)
// ---------------------------------------------------------------------------

// failingTeardownWorker is a worker whose Stop method returns a non-nil error
// without timing out. It is used to exercise the teardown_error outcome branch.
type failingTeardownWorker struct {
	stopErr error
}

func (w *failingTeardownWorker) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (w *failingTeardownWorker) Stop(_ context.Context) error {
	return w.stopErr
}

// TestShutdownMetrics_Outcome_TeardownError verifies that when a teardown
// returns an error (without shutCtx expiring), the outcome counter records
// outcome="teardown_error" rather than masking it as success.
func TestShutdownMetrics_Outcome_TeardownError(t *testing.T) {
	p := newFakeMetricsProvider()
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "teardown-err", DurabilityMode: cell.DurabilityDemo})

	failWorker := &failingTeardownWorker{stopErr: fmt.Errorf("simulated teardown failure")}

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(3*time.Second),
		WithMetricsProvider(p),
		WithWorkers(failWorker),
	)

	err := runWithCancelAndListener(t, b, ln, 5*time.Second)
	require.Error(t, err, "Run must surface the teardown error")

	outcomeVec := p.counter(shutdownTotalCounterName)
	require.NotNil(t, outcomeVec, "outcome counter must be registered")

	assert.Equal(t, float64(1), outcomeVec.totalForLabel("outcome", "teardown_error"),
		"outcome=teardown_error must be incremented when a teardown fails within the shutdown budget")
	assert.Equal(t, float64(0), outcomeVec.totalForLabel("outcome", "success"),
		"outcome=success must not be incremented when a teardown fails")
	assert.Equal(t, float64(0), outcomeVec.totalForLabel("outcome", "timeout"),
		"outcome=timeout must not be incremented when only a teardown failed")
}

// TestShutdownMetrics_Outcome_SignalError verifies that when a worker returns
// an error while the app is running (triggering phase10), and teardown itself
// completes cleanly, the outcome counter records outcome="signal_error".
// This distinguishes "shutdown-triggered-by-failure" from "user-initiated-shutdown".
func TestShutdownMetrics_Outcome_SignalError(t *testing.T) {
	p := newFakeMetricsProvider()
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "signal-err", DurabilityMode: cell.DurabilityDemo})

	// Worker returns an error shortly after Start — triggers phase9 signal
	// with a non-nil err, then phase10 teardown runs cleanly.
	errWorker := &erroringWorker{err: fmt.Errorf("simulated worker crash")}

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(3*time.Second),
		WithMetricsProvider(p),
		WithWorkers(errWorker),
	)

	errCh := make(chan error, 1)
	go func() { errCh <- b.Run(context.Background()) }()

	select {
	case err := <-errCh:
		require.Error(t, err, "Run must surface worker error")
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after worker error")
	}

	outcomeVec := p.counter(shutdownTotalCounterName)
	require.NotNil(t, outcomeVec)

	assert.Equal(t, float64(1), outcomeVec.totalForLabel("outcome", "signal_error"),
		"outcome=signal_error must be incremented when worker error triggers phase10 and teardown is clean")
	assert.Equal(t, float64(0), outcomeVec.totalForLabel("outcome", "success"),
		"outcome=success must not be incremented when worker error triggered shutdown")
}

// erroringWorker returns an error from Start after a brief delay. Used to
// simulate a runtime worker crash (phase9 signal.err != nil).
type erroringWorker struct {
	err error
}

func (w *erroringWorker) Start(_ context.Context) error {
	// Small delay so Run reaches phase9 select; then error triggers shutdown.
	time.Sleep(50 * time.Millisecond)
	return w.err
}

func (w *erroringWorker) Stop(_ context.Context) error { return nil }

// ---------------------------------------------------------------------------
// Test 5: no panic without provider (NopProvider default)
// ---------------------------------------------------------------------------

// TestShutdownMetrics_DisabledWithoutProvider verifies that phase10 completes
// normally when no metrics provider is configured (NopProvider default).
func TestShutdownMetrics_DisabledWithoutProvider(t *testing.T) {
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "nop-sm", DurabilityMode: cell.DurabilityDemo})
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(3*time.Second),
		// No WithMetricsProvider — defaults to NopProvider.
	)
	require.NoError(t, runWithCancelAndListener(t, b, ln, 5*time.Second))
}

// ---------------------------------------------------------------------------
// Test 6: nil-safety of shutdownMetrics methods (unit level)
// ---------------------------------------------------------------------------

// TestShutdownMetrics_NilSafe verifies that all shutdownMetrics methods are
// nil-safe and do not panic when called on a nil receiver.
func TestShutdownMetrics_NilSafe(t *testing.T) {
	var m *shutdownMetrics
	require.NotPanics(t, func() {
		m.recordPhaseEntry(shutdownPhaseReadinessFlip)
		m.observePhaseDuration("readiness_flip", 1*time.Millisecond)
		m.countOutcome("success")
	})
}

// ---------------------------------------------------------------------------
// Test 7: newShutdownMetrics with nil provider returns nil
// ---------------------------------------------------------------------------

func TestNewShutdownMetrics_NilProvider(t *testing.T) {
	m, err := newShutdownMetrics(nil)
	require.NoError(t, err)
	assert.Nil(t, m, "nil provider must return nil shutdownMetrics")
}

// ---------------------------------------------------------------------------
// Test 8: concurrent observations do not race
// ---------------------------------------------------------------------------

// TestShutdownMetrics_ConcurrentObserve verifies that concurrent calls to
// shutdownMetrics methods do not cause data races (exercised via -race).
func TestShutdownMetrics_ConcurrentObserve(t *testing.T) {
	p := newFakeMetricsProvider()
	m, err := newShutdownMetrics(p)
	require.NoError(t, err)
	require.NotNil(t, m)

	var wg sync.WaitGroup
	var panicked atomic.Bool
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked.Store(true)
				}
			}()
			m.recordPhaseEntry(shutdownPhaseReadinessFlip)
			m.observePhaseDuration("readiness_flip", time.Millisecond)
			m.countOutcome("success")
		}()
	}
	wg.Wait()
	assert.False(t, panicked.Load(), "concurrent calls must not panic")
}
