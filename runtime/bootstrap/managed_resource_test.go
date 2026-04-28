package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/runtime/http/health"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResource is a test implementation of ManagedResource.
type fakeResource struct {
	name     string
	checkErr error
	worker   kworker.Worker
	closeErr error
	closed   bool
}

func (f *fakeResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		f.name: func(_ context.Context) error { return f.checkErr },
	}
}

func (f *fakeResource) Worker() kworker.Worker { return f.worker }

func (f *fakeResource) Close(_ context.Context) error {
	f.closed = true
	return f.closeErr
}

// fakeWorker is a minimal kworker.Worker for testing.
type fakeWorker struct {
	started bool
	stopped bool
	stopCh  chan struct{}
}

func newFakeWorker() *fakeWorker {
	return &fakeWorker{stopCh: make(chan struct{})}
}

func (w *fakeWorker) Start(ctx context.Context) error {
	w.started = true
	// Block until stopped or context cancelled.
	select {
	case <-ctx.Done():
	case <-w.stopCh:
	}
	return nil
}

func (w *fakeWorker) Stop(_ context.Context) error {
	w.stopped = true
	close(w.stopCh)
	return nil
}

// TestManagedResource_RegistersHealthChecker verifies that a resource registered
// via WithManagedResource contributes its checkers to /readyz.
func TestManagedResource_RegistersHealthChecker(t *testing.T) {
	res := &fakeResource{name: "fake-pg", checkErr: nil}

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res),
		WithHealthRoutes(WithReadyzVerboseToken(testVerboseToken)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	defer func() {
		cancel()
		<-errCh
	}()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// /readyz?verbose should include the "fake-pg" checker name in the body,
	// proving the checker was actually registered (not just that readyz returns 200).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/readyz?verbose=true", addr), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	req.Header.Set(health.VerboseTokenHeader, testVerboseToken)
	resp, err := testHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("GET /readyz?verbose failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fake-pg") {
		t.Errorf("expected /readyz?verbose body to contain checker name %q, got: %s", "fake-pg", string(body))
	}
}

// TestManagedResource_RegistersWorker verifies that a resource worker is started
// and stopped by the bootstrap WorkerGroup.
func TestManagedResource_RegistersWorker(t *testing.T) {
	fw := newFakeWorker()
	res := &fakeResource{name: "worker-res", worker: fw}

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Worker must have been started (bootstrap starts workers in Step 8) and
	// stopped (bootstrap stops workers during shutdown). Both sides of the
	// lifecycle must be exercised as the test comment states.
	if !fw.started {
		t.Error("expected worker Start to be called before shutdown")
	}
	if !fw.stopped {
		t.Error("expected worker Stop to be called during shutdown")
	}
}

// TestManagedResource_LIFOClose verifies that multiple resources are closed in
// LIFO (last-registered-first-closed) order.
func TestManagedResource_LIFOClose(t *testing.T) {
	var closeOrder []string

	makeRes := func(name string) *trackingResource {
		return &trackingResource{
			name:       name,
			closeOrder: &closeOrder,
		}
	}

	res1 := makeRes("first")
	res2 := makeRes("second")
	res3 := makeRes("third")

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res1),
		WithManagedResource(res2),
		WithManagedResource(res3),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("unexpected shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// LIFO: third should be closed first, then second, then first.
	if len(closeOrder) != 3 {
		t.Fatalf("expected 3 closes, got %d: %v", len(closeOrder), closeOrder)
	}
	if closeOrder[0] != "third" || closeOrder[1] != "second" || closeOrder[2] != "first" {
		t.Errorf("expected LIFO close order [third, second, first], got %v", closeOrder)
	}
}

// trackingResource records its Close call order.
type trackingResource struct {
	name       string
	closeOrder *[]string
}

func (r *trackingResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		r.name: func(_ context.Context) error { return nil },
	}
}

func (r *trackingResource) Worker() kworker.Worker { return nil }

func (r *trackingResource) Close(_ context.Context) error {
	*r.closeOrder = append(*r.closeOrder, r.name)
	return nil
}

// TestManagedResource_NilWorkerNoOp verifies that a resource with a nil worker
// does not register any worker and does not produce errors.
func TestManagedResource_NilWorkerNoOp(t *testing.T) {
	res := &fakeResource{name: "no-worker-res", worker: nil}

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil error with nil worker resource, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestManagedResource_CloseErrorPropagates verifies that a Close error is logged
// but does not prevent other resources from being closed (best-effort Close).
func TestManagedResource_CloseErrorPropagates(t *testing.T) {
	closeErr := errors.New("simulated close failure")
	res1 := &fakeResource{name: "bad-res", closeErr: closeErr}
	res2 := &trackingResource{name: "good-res", closeOrder: new([]string)}

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res1),
		WithManagedResource(res2),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	cancel()
	select {
	case <-errCh:
		// We don't assert on the error value here — ManagedResource.Close errors
		// are best-effort (logged as Warn, do not block other resources).
		// The important assertion is that res2 is still closed despite res1 failing.
		if !res1.closed {
			t.Error("res1 Close() must be called even if it returns an error")
		}
		if len(*res2.closeOrder) == 0 {
			t.Error("res2 Close() must be called even when res2 (registered after res1) triggers LIFO first")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestWithManagedResource_NilFailFast verifies that WithManagedResource(nil)
// sets the managedResourceNil flag and Run() rejects it at phase0 before any
// side effects. Mirrors the WithCircuitBreaker fail-fast pattern.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go Append — hook registration
// does no nil-substitution; bad inputs surface before any component starts.
func TestWithManagedResource_NilFailFast(t *testing.T) {
	app := New(WithManagedResource(nil))
	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run must fail when WithManagedResource(nil) was used")
	}
	const want = "managed resource must not be nil"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q must contain %q", err.Error(), want)
	}
}

// TestWithManagedResource_TypedNilFailFast verifies that a typed-nil
// (non-nil interface wrapping a nil pointer) is detected at phase0 and
// causes Run() to return an error — mirrors
// TestWithCircuitBreaker_TypedNilPointer_Error.
//
// Without reflect-based detection, WithManagedResource((*fakeResource)(nil))
// would pass the `r == nil` guard and panic at Checkers()/Worker()/Close()
// call time during expandManagedResources() or shutdown.
func TestWithManagedResource_TypedNilFailFast(t *testing.T) {
	var res *fakeResource // typed nil
	var iface kernellifecycle.ManagedResource = res

	app := New(WithManagedResource(iface))
	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run must fail when WithManagedResource receives a typed-nil interface")
	}
	const want = "managed resource must not be nil"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q must contain %q", err.Error(), want)
	}
}

// TestManagedResource_CloseErrorPropagatesToPhase10 verifies that when a
// ManagedResource.Close() returns an error, that error is propagated to the
// Run() return value via phase10 teardown aggregation.
//
// Previously the closure signature was `func(ctx context.Context)` (no error
// return), which silently swallowed Close errors after slog.Warn. This test
// ensures the error surfaces to the Run() caller so operators see a non-clean
// shutdown when a resource fails to close.
func TestManagedResource_CloseErrorPropagatesToPhase10(t *testing.T) {
	closeErr := errors.New("simulated close failure")
	res := &fakeResource{name: "bad-res", closeErr: closeErr}

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	cancel()
	select {
	case runErr := <-errCh:
		if runErr == nil {
			t.Fatal("expected Run to return an error when Close fails, got nil")
		}
		if !strings.Contains(runErr.Error(), "simulated close failure") {
			t.Errorf("Run error %q must contain %q", runErr.Error(), "simulated close failure")
		}
		if !strings.Contains(runErr.Error(), "*bootstrap.fakeResource") {
			t.Errorf("Run error %q must contain managed resource type", runErr.Error())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// Relay-as-ManagedResource tests (TM2/TM3/TM4)
// Migrated from bootstrap_test.go TestWithRelayHealth_* equivalents.
// These tests use WithManagedResource(relay) instead of the deleted WithRelayHealth.
// ---------------------------------------------------------------------------

// newManagedResourceTestRelay creates a Relay with all three failure budgets enabled,
// suitable for ManagedResource integration tests.
func newManagedResourceTestRelay() *runtimeoutbox.Relay {
	cfg := runtimeoutbox.RelayConfig{
		PollInterval:         5 * time.Millisecond,
		ReclaimInterval:      10 * time.Millisecond,
		BatchSize:            10,
		MaxAttempts:          3,
		BaseRetryDelay:       1 * time.Millisecond,
		MaxRetryDelay:        10 * time.Millisecond,
		ClaimTTL:             100 * time.Millisecond,
		RetentionPeriod:      1 * time.Hour,
		DeadRetentionPeriod:  24 * time.Hour,
		CleanupWaitFloor:     5 * time.Millisecond,
		PollFailureBudget:    3,
		ReclaimFailureBudget: 3,
		CleanupFailureBudget: 3,
	}
	return runtimeoutbox.NewRelay(outboxtest.NewFakeStore(), &koutbox.DiscardPublisher{}, cfg)
}

// managedResourceFailingStore wraps FakeStore to inject controllable ClaimPending errors.
type managedResourceFailingStore struct {
	*outboxtest.FakeStore
	mu       sync.Mutex
	claimErr error
}

func (s *managedResourceFailingStore) setClaimErr(err error) {
	s.mu.Lock()
	s.claimErr = err
	s.mu.Unlock()
}

func (s *managedResourceFailingStore) ClaimPending(ctx context.Context, batchSize int) ([]runtimeoutbox.ClaimedEntry, error) {
	s.mu.Lock()
	err := s.claimErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.FakeStore.ClaimPending(ctx, batchSize)
}

// TM2: TestRelay_AsManagedResource_RegistersCheckers verifies that a Relay registered
// via WithManagedResource contributes its three health checkers to /readyz?verbose.
// Migrated from TestWithRelayHealth_RegistersCheckers.
func TestRelay_AsManagedResource_RegistersCheckers(t *testing.T) {
	ln := newLocalListener(t)

	asm := assembly.New(assembly.Config{ID: "test-relay-mr-checkers", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	relay := newManagedResourceTestRelay()

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(2*time.Second),
		WithManagedResource(relay),
		WithHealthRoutes(WithReadyzVerboseToken(testVerboseToken)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// GET /readyz?verbose — all three relay checkers must appear.
	resp, err := verboseGet(ctx, fmt.Sprintf("http://%s", addr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	deps, ok := readyzPayload(t, body)["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")

	assert.Contains(t, deps, "outbox-relay-poll", "poll checker must be in /readyz?verbose")
	assert.Contains(t, deps, "outbox-relay-reclaim", "reclaim checker must be in /readyz?verbose")
	assert.Contains(t, deps, "outbox-relay-cleanup", "cleanup checker must be in /readyz?verbose")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TM3: TestRelay_AsManagedResource_TrippedBudget_Returns503 verifies the P1-15 core contract:
// poll budget trip → /readyz returns 503; store recovery → /readyz returns 200.
// Migrated from TestWithRelayHealth_TrippedBudget_Returns503.
func TestRelay_AsManagedResource_TrippedBudget_Returns503(t *testing.T) {
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "test-relay-mr-trip", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	store := &managedResourceFailingStore{FakeStore: outboxtest.NewFakeStore()}
	store.setClaimErr(errors.New("db down"))

	cfg := runtimeoutbox.RelayConfig{
		PollInterval:         5 * time.Millisecond,
		ReclaimInterval:      10 * time.Millisecond,
		BatchSize:            10,
		MaxAttempts:          3,
		BaseRetryDelay:       1 * time.Millisecond,
		MaxRetryDelay:        10 * time.Millisecond,
		ClaimTTL:             100 * time.Millisecond,
		RetentionPeriod:      1 * time.Hour,
		DeadRetentionPeriod:  24 * time.Hour,
		CleanupWaitFloor:     5 * time.Millisecond,
		PollFailureBudget:    3,
		ReclaimFailureBudget: 3,
		CleanupFailureBudget: 3,
	}
	relay := runtimeoutbox.NewRelay(store, &koutbox.DiscardPublisher{}, cfg)

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(2*time.Second),
		WithManagedResource(relay),
		WithHealthRoutes(WithReadyzVerboseToken(testVerboseToken)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("bootstrap did not shut down in time")
		}
	}()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// TM3: bootstrap WorkerGroup is the single startup path for the relay.
	// Wait for the relay to reach the running state before asserting poll-budget behaviour.
	select {
	case <-relay.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for relay to become ready via bootstrap WorkerGroup")
	}

	// Phase 1: store failing — budget trips — /readyz must return 503.
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusServiceUnavailable
	}, 3*time.Second, 20*time.Millisecond, "/readyz must return 503 after poll budget trips")

	// Verify verbose output contains the unhealthy checker name.
	verboseResp, err := verboseGet(ctx, fmt.Sprintf("http://%s", addr))
	require.NoError(t, err)
	defer verboseResp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, verboseResp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(verboseResp.Body).Decode(&body))
	details := assertReadyzServiceUnavailable(t, body, "unhealthy", "readiness_failed")
	deps, ok := details["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	require.Contains(t, deps, "outbox-relay-poll", "poll checker must appear in verbose output")
	pollProbe, ok := deps["outbox-relay-poll"].(map[string]any)
	require.True(t, ok, "outbox-relay-poll must be a structured ProbeResult")
	assert.Equal(t, "unhealthy", pollProbe["status"], "outbox-relay-poll: status must be unhealthy")

	// Phase 2: store recovers — budget resets — /readyz must return 200.
	store.setClaimErr(nil)
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 20*time.Millisecond, "/readyz must return 200 after store recovers")
}

// TM4: TestRelay_AsManagedResource_DisabledBudget_SkipsChecker verifies that a relay with
// poll budget disabled (threshold=0) does not register the outbox-relay-poll checker.
// Migrated from TestWithRelayHealth_DisabledBudget_SkipsChecker.
func TestRelay_AsManagedResource_DisabledBudget_SkipsChecker(t *testing.T) {
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "test-relay-mr-disabled", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	// Poll budget disabled (=0), others enabled.
	cfg := runtimeoutbox.RelayConfig{
		PollInterval:         5 * time.Millisecond,
		ReclaimInterval:      10 * time.Millisecond,
		BatchSize:            10,
		MaxAttempts:          3,
		BaseRetryDelay:       1 * time.Millisecond,
		MaxRetryDelay:        10 * time.Millisecond,
		ClaimTTL:             100 * time.Millisecond,
		RetentionPeriod:      1 * time.Hour,
		DeadRetentionPeriod:  24 * time.Hour,
		CleanupWaitFloor:     5 * time.Millisecond,
		PollFailureBudget:    0, // disabled
		ReclaimFailureBudget: 3,
		CleanupFailureBudget: 3,
	}
	relay := runtimeoutbox.NewRelay(outboxtest.NewFakeStore(), &koutbox.DiscardPublisher{}, cfg)

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(2*time.Second),
		WithManagedResource(relay),
		WithHealthRoutes(WithReadyzVerboseToken(testVerboseToken)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, err := verboseGet(ctx, fmt.Sprintf("http://%s", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	deps, _ := readyzPayload(t, body)["dependencies"].(map[string]any)

	assert.NotContains(t, deps, "outbox-relay-poll",
		"disabled poll budget must not register a checker")
	assert.Contains(t, deps, "outbox-relay-reclaim")
	assert.Contains(t, deps, "outbox-relay-cleanup")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// Unit tests for expandManagedResources — reverse/negative assertions (T-1)
// ---------------------------------------------------------------------------

// nilWorkerResource is a ManagedResource whose Worker() always returns nil.
type nilWorkerResource struct{}

func (nilWorkerResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"nil-worker-checker": func(_ context.Context) error { return nil },
	}
}

func (nilWorkerResource) Worker() kworker.Worker { return nil }

func (nilWorkerResource) Close(_ context.Context) error { return nil }

// TestExpandManagedResources_NilWorker_Skip verifies that a ManagedResource
// whose Worker() returns nil does not register any worker into the Bootstrap
// worker pool. The checker must still be registered (nil-worker is not
// nil-resource; only the background goroutine is omitted).
func TestExpandManagedResources_NilWorker_Skip(t *testing.T) {
	b := &Bootstrap{}
	b.lifecycle.managedResources = []kernellifecycle.ManagedResource{nilWorkerResource{}}

	require.NoError(t, b.expandManagedResources())

	assert.Len(t, b.http.healthCheckers, 1, "checker must still be registered when worker is nil")
	assert.Empty(t, b.events.workers, "nil worker must not be added to worker pool")
	assert.Len(t, b.lifecycle.managedResourceTeardowns, 1, "teardown must still be registered for Close")
}

// duplicateCheckerResource provides a single checker under a fixed key name.
type duplicateCheckerResource struct{ key string }

func (r duplicateCheckerResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		r.key: func(_ context.Context) error { return nil },
	}
}

func (r duplicateCheckerResource) Worker() kworker.Worker { return nil }

func (r duplicateCheckerResource) Close(_ context.Context) error { return nil }

// TestExpandManagedResources_DuplicateChecker_Phase0Error verifies that two
// ManagedResources exposing the same checker key cause expandManagedResources
// to return a non-nil error containing "duplicate checker". This prevents a
// silent registration collision where the second checker silently shadows the
// first and health misreporting goes undetected until production.
func TestExpandManagedResources_DuplicateChecker_Phase0Error(t *testing.T) {
	b := &Bootstrap{}
	b.lifecycle.managedResources = []kernellifecycle.ManagedResource{
		duplicateCheckerResource{key: "db"},
		duplicateCheckerResource{key: "db"},
	}

	err := b.expandManagedResources()
	require.Error(t, err, "duplicate checker key must cause expandManagedResources to fail")
	assert.Contains(t, err.Error(), "duplicate checker",
		"error message must name the conflict so operators can identify the culprit")
}

// TestExpandManagedResources_CloseFailure_TeardownChainContinues verifies that
// when the first registered resource's Close returns an error, the teardown
// chain still invokes Close on all remaining resources (LIFO). This matches
// the documented best-effort close semantics: errors are logged as Warn and
// the shutdown continues rather than short-circuiting.
func TestExpandManagedResources_CloseFailure_TeardownChainContinues(t *testing.T) {
	var closedOrder []string

	firstClose := func(ctx context.Context) error {
		closedOrder = append(closedOrder, "first")
		return errors.New("first close failed")
	}

	secondClosed := false
	secondClose := func(ctx context.Context) error {
		closedOrder = append(closedOrder, "second")
		secondClosed = true
		return nil
	}

	b := &Bootstrap{}
	b.lifecycle.managedResources = []kernellifecycle.ManagedResource{
		&orderedCloseResource{name: "first", closeFn: firstClose},
		&orderedCloseResource{name: "second", closeFn: secondClose},
	}

	require.NoError(t, b.expandManagedResources())
	require.Len(t, b.lifecycle.managedResourceTeardowns, 2)

	// Teardowns are in registration order; LIFO means we call them reversed.
	ctx := context.Background()
	for i := len(b.lifecycle.managedResourceTeardowns) - 1; i >= 0; i-- {
		_ = b.lifecycle.managedResourceTeardowns[i].fn(ctx) // ignore individual errors; chain must continue
	}

	// LIFO: second registered → first closed; then first registered → closed second.
	assert.Equal(t, []string{"second", "first"}, closedOrder,
		"LIFO teardown must close second before first")
	assert.True(t, secondClosed,
		"second resource Close must be called even though first resource Close failed")
}

func TestExpandManagedResources_CloseFailure_TeardownErrorIncludesResourceType(t *testing.T) {
	b := &Bootstrap{}
	b.lifecycle.managedResources = []kernellifecycle.ManagedResource{
		&orderedCloseResource{
			name: "typed-resource",
			closeFn: func(context.Context) error {
				return errors.New("close failed")
			},
		},
	}

	require.NoError(t, b.expandManagedResources())
	require.Len(t, b.lifecycle.managedResourceTeardowns, 1)

	_, s := newPhaseState()
	for _, td := range b.lifecycle.managedResourceTeardowns {
		s.addNamedTeardown(td.name, td.fn)
	}

	errs := New().phase10LIFOTeardown(context.Background(), s)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "*bootstrap.orderedCloseResource")

	var pe *phaseError
	require.True(t, errors.As(errs[0], &pe), "managed resource teardown error must use named phase wrapping")
	assert.Contains(t, pe.Phase, "*bootstrap.orderedCloseResource")
}

// orderedCloseResource is a ManagedResource test double that delegates Close
// to an injected function so tests can capture close ordering and errors.
type orderedCloseResource struct {
	name    string
	closeFn func(context.Context) error
}

func (r *orderedCloseResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		r.name: func(_ context.Context) error { return nil },
	}
}

func (r *orderedCloseResource) Worker() kworker.Worker { return nil }

func (r *orderedCloseResource) Close(ctx context.Context) error {
	if r.closeFn != nil {
		return r.closeFn(ctx)
	}
	return nil
}

// sequencedResource records the monotonic sequence number at which Close() was
// invoked. Used by TestManagedResource_LIFOCloseBySequence to assert that the
// last-registered resource closes before the first-registered resource,
// satisfying the MODULE-ORDER-CLOSE-LIFO contract documented in
// cmd/corebundle/shared_deps.go (SharedPGPool happens-before contract).
//
// A shared counter is incremented on each Close(); the sequence number
// captured by each resource reflects call order without relying on wall-clock
// granularity (two synchronous Close() calls in the same teardown loop can
// share the same nanosecond, making timestamp comparison unreliable).
type sequencedResource struct {
	name     string
	counter  *atomic.Int64 // shared across resources; incremented on each Close
	closeSeq atomic.Int64  // sequence number captured at Close(); 0 = not yet closed
}

func (r *sequencedResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		r.name: func(_ context.Context) error { return nil },
	}
}

func (r *sequencedResource) Worker() kworker.Worker { return nil }

func (r *sequencedResource) Close(_ context.Context) error {
	seq := r.counter.Add(1) // returns new value (1-based)
	r.closeSeq.Store(seq)
	return nil
}

// TestManagedResource_LIFOCloseBySequence asserts the MODULE-ORDER-CLOSE-LIFO
// contract using monotonic sequence numbers: the resource registered last
// (worker, simulating a ConsumerBase / OutboxRelay) must have its Close()
// invoked before the resource registered first (pgRes, simulating SharedPGPool).
//
// This locks in the bootstrap LIFO guarantee that protects against
// use-after-close DB calls: if a consumer worker still holds an open DB
// transaction when pool.Close() is called, the next DB call will fail or panic.
// LIFO order ensures all consumer workers are stopped before the pool is closed.
//
// Ref: cmd/corebundle/shared_deps.go SharedPGPool "Happens-before contract".
func TestManagedResource_LIFOCloseBySequence(t *testing.T) {
	var counter atomic.Int64
	pgRes := &sequencedResource{name: "fake-pg-pool", counter: &counter}
	worker := &sequencedResource{name: "fake-consumer-worker", counter: &counter}

	ln := newLocalListener(t)
	app := New(
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		// pgRes registered FIRST (simulating ConfigCoreModule.Provide).
		WithManagedResource(pgRes),
		// worker registered SECOND (simulating a later consumer module / WithWorkers).
		WithManagedResource(worker),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	waitForHealthy(t, ln.Addr().String())

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("unexpected Run error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap.Run did not exit within 5s after cancel")
	}

	pgSeq := pgRes.closeSeq.Load()
	workerSeq := worker.closeSeq.Load()
	if pgSeq == 0 {
		t.Fatal("pgRes.Close was never invoked")
	}
	if workerSeq == 0 {
		t.Fatal("worker.Close was never invoked")
	}
	// LIFO: worker (registered last) must close first → smaller sequence number.
	if workerSeq >= pgSeq {
		t.Errorf(
			"MODULE-ORDER-CLOSE-LIFO contract violated: worker.Close (registered last, seq=%d) "+
				"must execute BEFORE pgRes.Close (registered first, seq=%d). "+
				"LIFO ordering is the bootstrap's only guarantee that consumer workers "+
				"don't see a closed pool. See cmd/corebundle/shared_deps.go SharedPGPool "+
				"happens-before contract.",
			workerSeq, pgSeq,
		)
	}
}
