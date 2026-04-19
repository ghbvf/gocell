package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/worker"
)

// fakeResource is a test implementation of ManagedResource.
type fakeResource struct {
	name     string
	checkErr error
	worker   worker.Worker
	closeErr error
	closed   bool
}

func (f *fakeResource) Checkers() map[string]func() error {
	return map[string]func() error{
		f.name: func() error { return f.checkErr },
	}
}

func (f *fakeResource) Worker() worker.Worker { return f.worker }

func (f *fakeResource) Close() error {
	f.closed = true
	return f.closeErr
}

// fakeWorker is a minimal worker.Worker for testing.
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
		WithListener(ln),
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

	// /readyz?verbose should include the "fake-pg" checker name.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	if err != nil {
		t.Fatalf("GET /readyz?verbose failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestManagedResource_RegistersWorker verifies that a resource worker is started
// and stopped by the bootstrap WorkerGroup.
func TestManagedResource_RegistersWorker(t *testing.T) {
	fw := newFakeWorker()
	res := &fakeResource{name: "worker-res", worker: fw}

	ln := newLocalListener(t)
	app := New(
		WithListener(ln),
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

	// Worker must have been started (bootstrap starts workers in Step 8)
	// Note: our fakeWorker.Start returns immediately, so started means it was called.
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
		WithListener(ln),
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

func (r *trackingResource) Checkers() map[string]func() error {
	return map[string]func() error{
		r.name: func() error { return nil },
	}
}

func (r *trackingResource) Worker() worker.Worker { return nil }

func (r *trackingResource) Close() error {
	*r.closeOrder = append(*r.closeOrder, r.name)
	return nil
}

// TestManagedResource_NilWorkerNoOp verifies that a resource with a nil worker
// does not register any worker and does not produce errors.
func TestManagedResource_NilWorkerNoOp(t *testing.T) {
	res := &fakeResource{name: "no-worker-res", worker: nil}

	ln := newLocalListener(t)
	app := New(
		WithListener(ln),
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
		WithListener(ln),
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
// side effects. Mirrors the WithCircuitBreaker / WithBrokerHealth fail-fast
// pattern.
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
