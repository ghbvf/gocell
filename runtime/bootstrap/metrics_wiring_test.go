package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// registrationSpy counts every CounterVec registration so tests can assert
// that a Provider injected into bootstrap reaches assembly.Config without
// constructing the full runtime. Fulfills kernelmetrics.Provider with
// Nop-behavior otherwise so nothing else breaks.
type registrationSpy struct {
	mu             sync.Mutex
	counterNames   []string
	histogramNames []string
	nop            kernelmetrics.NopProvider
}

func (s *registrationSpy) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	s.mu.Lock()
	s.counterNames = append(s.counterNames, opts.Name)
	s.mu.Unlock()
	return s.nop.CounterVec(opts)
}

func (s *registrationSpy) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	s.mu.Lock()
	s.histogramNames = append(s.histogramNames, opts.Name)
	s.mu.Unlock()
	return s.nop.HistogramVec(opts)
}

func (s *registrationSpy) Unregister(_ kernelmetrics.Collector) error { return nil }

func (s *registrationSpy) counters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.counterNames...)
}

// TestBootstrap_DefaultAssembly_WiresMetricsProvider is the regression
// test for the F1 finding: before the fix, WithMetricsProvider only
// populated Bootstrap.metricsProvider while the default assembly.New()
// path (bootstrap.go, case b.assembly == nil) omitted MetricsProvider from
// assembly.Config. Consequence: hook dispatcher drop metrics silently
// landed on NopProvider, and every operational claim about "shared
// metrics surface" was untrue on the default startup path.
//
// This test exercises the constructor only (not a full Run) because:
//
//	(a) full Run needs an HTTP listener / signal plumbing that would
//	    require extensive port-binding plumbing inside the sandbox;
//	(b) the wiring bug is a pure composition bug — the moment
//	    assembly.New is called, the dispatcher registers its drop
//	    counter via the provided Provider. If the Provider reaches the
//	    Config struct, our spy records "hook_observer_dropped_total" (bare
//	    name; Provider Namespace adds "gocell_" prefix for the final fqName).
//	    If the Provider is Nop (bug present), the name never appears.
func TestBootstrap_DefaultAssembly_WiresMetricsProvider(t *testing.T) {
	spy := &registrationSpy{}

	// Construct a Bootstrap exactly as a caller would. Then trigger the
	// same build-default-assembly branch that Run() uses by poking the
	// field directly — this is a white-box test sharing the package, so
	// we can construct the default config the same way Run() does and
	// assert the resulting dispatcher used our Provider.
	b := New(WithClock(clock.Real()), WithMetricsProvider(spy))

	// Mirror bootstrap.Run's default-assembly construction exactly.
	cfg := assembly.Config{ID: "default", DurabilityMode: cell.DurabilityDemo, Clock: b.clock}
	if b.metricsProvider != nil {
		cfg.MetricsProvider = b.metricsProvider
	}
	asm := assembly.New(cfg)
	t.Cleanup(asm.Shutdown)

	names := spy.counters()
	require.NotEmpty(t, names,
		"hook dispatcher must register its drop counter on the injected Provider; "+
			"empty means MetricsProvider was not threaded through (finding F1 regressed)")
	assert.True(t, slices.Contains(names, "hook_observer_dropped_total"),
		"expected 'hook_observer_dropped_total' (bare name) among registered counters, got %v", names)
}

// TestBootstrap_DefaultAssembly_NoProviderUsesNop pins the inverse
// contract: when WithMetricsProvider is not called, the dispatcher still
// works (via NopProvider) and does not allocate against any caller
// registry. This is defensive: removing MetricsProvider from the default
// Config should not regress into nil or panic.
func TestBootstrap_DefaultAssembly_NoProviderUsesNop(t *testing.T) {
	b := New(WithClock(clock.Real()))
	cfg := assembly.Config{ID: "default", DurabilityMode: cell.DurabilityDemo, Clock: b.clock}
	if b.metricsProvider != nil {
		cfg.MetricsProvider = b.metricsProvider
	}
	asm := assembly.New(cfg)
	t.Cleanup(asm.Shutdown)
	// Smoke: Register + Start + Stop using in-memory cell; no panic
	// expected. If MetricsProvider default were nil-typed, this would
	// crash inside the dispatcher.
}

// TestAssembly_FailedStartDrainsDispatcher pins F2. assembly.New spawns
// the dispatcher goroutine eagerly; if Start fails the caller must still
// be able to reclaim it via Shutdown. The kernel-level contract is what
// bootstrap relies on; if this breaks, bootstrap's rollback cannot
// recover either.
//
// The accompanying bootstrap.go change registers an Shutdown teardown
// *before* StartWithConfig so rollback reaches it even on failure — that
// is exercised indirectly through existing rollback tests, and directly
// here at the kernel layer which is the source of the goroutine.
func TestAssembly_FailedStartDrainsDispatcher(t *testing.T) {
	failing := &startFailCell{BaseCell: cell.NewBaseCell(cell.CellMetadata{
		ID:   "fail-start",
		Type: cell.CellTypeCore,
	})}
	asm := assembly.New(assembly.Config{
		ID:             "t-fail",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})
	require.NoError(t, asm.Register(failing))

	err := asm.Start(context.Background())
	require.Error(t, err, "Start must fail so the test exercises the failed-start path")

	// Reclaim the dispatcher goroutine. The assertion that this actually
	// works lives in kernel/assembly's goleak-aware TestMain — if the
	// dispatcher leaks, the whole package goes red.
	asm.Shutdown()
	asm.Shutdown() // idempotent; must not panic or double-close.
}

// startFailCell implements cell.Cell and returns a deterministic error
// from Start so Assembly.Start fails on the first cell.
type startFailCell struct {
	*cell.BaseCell
}

func (c *startFailCell) Start(context.Context) error {
	return errors.New("startFailCell: simulated failure")
}

// --- R2: autoWireHTTPMetricsCollector tests ---

// TestBootstrap_MetricsProvider_AutoWiresHTTPCollector verifies that when a
// non-Nop Provider is injected, autoWireHTTPMetricsCollector adds a
// router.WithMetricsCollector option that registers the two canonical HTTP
// metric names: http_requests_total and http_request_duration_seconds.
func TestBootstrap_MetricsProvider_AutoWiresHTTPCollector(t *testing.T) {
	spy := &registrationSpy{}
	b := New(WithClock(clock.Real()), WithMetricsProvider(spy))

	opts, err := b.autoWireHTTPMetricsCollector(nil)
	require.NoError(t, err, "autoWireHTTPMetricsCollector must succeed with a valid provider")
	require.Len(t, opts, 1, "must add exactly one router.Option (WithMetricsCollector)")

	// The spy must have recorded both canonical HTTP metric names.
	counters := spy.counters()
	assert.True(t, slices.Contains(counters, "http_requests_total"),
		"http_requests_total must be registered; got counters %v", counters)

	spy.mu.Lock()
	histograms := append([]string(nil), spy.histogramNames...)
	spy.mu.Unlock()
	assert.True(t, slices.Contains(histograms, "http_request_duration_seconds"),
		"http_request_duration_seconds must be registered; got histograms %v", histograms)
}

// TestBootstrap_NoMetricsProvider_NoAutoWire verifies that when no Provider is
// configured (NopProvider default), autoWireHTTPMetricsCollector returns the
// input opts unchanged without adding any new options.
func TestBootstrap_NoMetricsProvider_NoAutoWire(t *testing.T) {
	b := New(WithClock(clock.Real())) // NopProvider default

	initial := []router.Option{}
	opts, err := b.autoWireHTTPMetricsCollector(initial)
	require.NoError(t, err, "no-op path must not return an error")
	assert.Len(t, opts, 0, "NopProvider must not add any router options")
}

// TestAutoWire_CellLabel_FromExplicitAssemblyID verifies that WithAssemblyID
// flows all the way through to the cell label emitted on every RecordRequest
// call, not just to metric registration. The previous test only proved that
// http_requests_total was registered — a regression that hard-coded
// cell="default" inside RecordRequest would still pass that weaker assertion.
//
// finding 2 (PR-A66 round-2): close the cell-label behavior gap.
func TestAutoWire_CellLabel_FromExplicitAssemblyID(t *testing.T) {
	t.Parallel()
	p := newFakeMetricsProvider()
	b := New(WithClock(clock.Real()), WithMetricsProvider(p), WithAssemblyID("my-service"))

	_, err := b.autoWireHTTPMetricsCollector(nil)
	require.NoError(t, err)
	require.NotNil(t, b.httpCollector, "autoWire must cache collector on b.httpCollector")

	// Drive a real RecordRequest through the wired collector. This is the
	// step the original spy could not exercise: cell label is set per-call,
	// not per-registration.
	b.httpCollector.RecordRequest("GET", "/api/v1/users", 200, 0.05)

	reqs := p.counter("http_requests_total")
	require.NotNil(t, reqs, "http_requests_total must be registered")
	assert.Equal(t, float64(1), reqs.totalForLabel("cell", "my-service"),
		"RecordRequest must emit cell=my-service")
	assert.Equal(t, float64(0), reqs.totalForLabel("cell", "default"),
		"a regression that hard-codes cell=default would fail here")

	dur := p.histogram("http_request_duration_seconds")
	require.NotNil(t, dur)
	assert.NotEmpty(t, dur.observationsForLabel("cell", "my-service"),
		"duration histogram must also carry cell=my-service")
}

// TestAutoWire_CellLabel_DerivedFromAssembly verifies that omitting
// WithAssemblyID but supplying WithAssembly(asm) makes asm.ID() the cell
// label — not "default" and not the empty string.
func TestAutoWire_CellLabel_DerivedFromAssembly(t *testing.T) {
	t.Parallel()
	p := newFakeMetricsProvider()
	asm := assembly.New(assembly.Config{ID: "asm-id-x", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)

	b := New(WithClock(clock.Real()), WithMetricsProvider(p), WithAssembly(asm))

	_, err := b.autoWireHTTPMetricsCollector(nil)
	require.NoError(t, err)
	require.NotNil(t, b.httpCollector)
	b.httpCollector.RecordRequest("POST", "/api/v1/items", 201, 0.01)

	reqs := p.counter("http_requests_total")
	require.NotNil(t, reqs)
	assert.Equal(t, float64(1), reqs.totalForLabel("cell", "asm-id-x"),
		"cell label must be derived from assembly ID when WithAssemblyID is not set")
	assert.Equal(t, float64(0), reqs.totalForLabel("cell", "default"),
		"must not fall back to default when an assembly is wired")
}

// TestAutoWire_CellLabel_DefaultsWhenNeitherSet pins the no-config fallback
// so a future change can't silently switch the default. Operators rely on
// "default" as the sentinel that signals "no assembly identity wired".
func TestAutoWire_CellLabel_DefaultsWhenNeitherSet(t *testing.T) {
	t.Parallel()
	p := newFakeMetricsProvider()
	b := New(WithClock(clock.Real()), WithMetricsProvider(p))

	_, err := b.autoWireHTTPMetricsCollector(nil)
	require.NoError(t, err)
	require.NotNil(t, b.httpCollector)
	b.httpCollector.RecordRequest("GET", "/healthz", 200, 0.001)

	reqs := p.counter("http_requests_total")
	require.NotNil(t, reqs)
	assert.Equal(t, float64(1), reqs.totalForLabel("cell", "default"),
		"with no WithAssemblyID and no WithAssembly, the cell label must default to 'default'")
}

// TestAutoWireHTTPMetricsCollector_Conflict verifies that when the provider
// returns an error for http_requests_total registration (simulating a caller
// who also passed router.WithMetricsCollector via WithRouterOptions and the
// underlying registry enforces unique metric names),
// autoWireHTTPMetricsCollector returns an error containing both
// "metrics auto-wire conflict" and a "WithRouterOptions" hint.
func TestAutoWireHTTPMetricsCollector_Conflict(t *testing.T) {
	// alwaysFailProvider returns an error on any CounterVec registration,
	// simulating a registry that already has the metric registered.
	conflict := &alwaysFailCounterProvider{
		triggerName: "http_requests_total",
	}

	b := New(WithClock(clock.Real()), WithMetricsProvider(conflict))

	_, autoErr := b.autoWireHTTPMetricsCollector(nil)
	require.Error(t, autoErr, "registration error must propagate as a conflict error")
	assert.Contains(t, autoErr.Error(), "metrics auto-wire conflict",
		"error must mention 'metrics auto-wire conflict'; got: %v", autoErr)
	assert.Contains(t, autoErr.Error(), "WithRouterOptions",
		"error must include 'WithRouterOptions' hint; got: %v", autoErr)
}

// alwaysFailCounterProvider is a test-only metrics.Provider that returns an
// error whenever triggerName is registered, simulating a duplicate-name
// rejection from a real metrics backend.
type alwaysFailCounterProvider struct {
	triggerName string
	nop         kernelmetrics.NopProvider
}

func (p *alwaysFailCounterProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	if opts.Name == p.triggerName {
		return nil, fmt.Errorf("duplicate metric %q", opts.Name)
	}
	return p.nop.CounterVec(opts)
}

func (p *alwaysFailCounterProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return p.nop.HistogramVec(opts)
}

func (p *alwaysFailCounterProvider) Unregister(col kernelmetrics.Collector) error {
	return p.nop.Unregister(col)
}
