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
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	kworker "github.com/ghbvf/gocell/kernel/worker"
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
		WithManagedResource(res),
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
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(2*time.Second),
		WithManagedResource(relay),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// GET /readyz?verbose — all three relay checkers must appear.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(2*time.Second),
		WithManagedResource(relay),
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
	verboseResp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	require.NoError(t, err)
	defer verboseResp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, verboseResp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(verboseResp.Body).Decode(&body))
	deps, ok := readyzPayload(t, body)["dependencies"].(map[string]any)
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
		WithListener(cell.PrimaryListener, ln.Addr().String(), nil, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", nil, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(2*time.Second),
		WithManagedResource(relay),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
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
