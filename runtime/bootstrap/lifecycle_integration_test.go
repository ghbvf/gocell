//go:build integration

package bootstrap_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// integrationHTTPClient is used to prevent test hangs on stalled connections.
var integrationHTTPClient = &http.Client{Timeout: 2 * time.Second}

// newIntegrationListener creates a TCP listener on a random port.
func newIntegrationListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

// waitForIntegrationHealthy polls /healthz until it returns 200 or the timeout expires.
func waitForIntegrationHealthy(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := integrationHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "HTTP server did not become ready")
}

// TestLifecycleIntegration_HookStartStop_Ordering verifies that a registered
// Hook's OnStart completes before /healthz returns 200 (Step 4.6 precedes Step 7),
// and that OnStop is called after cancel() causes b.Run to return.
func TestLifecycleIntegration_HookStartStop_Ordering(t *testing.T) {
	var mu sync.Mutex
	var startedAt, stoppedAt time.Time

	ln := newIntegrationListener(t)
	addr := ln.Addr().String()

	var onStartCalled bool

	b := bootstrap.New(
		bootstrap.WithListener(ln),
		bootstrap.WithShutdownTimeout(3*time.Second),
		bootstrap.WithLifecycle(func(lc bootstrap.Lifecycle) {
			_ = lc.Append(bootstrap.Hook{
				Name: "timing-probe",
				OnStart: func(_ context.Context) error {
					mu.Lock()
					startedAt = time.Now()
					onStartCalled = true
					mu.Unlock()
					return nil
				},
				OnStop: func(_ context.Context) error {
					mu.Lock()
					stoppedAt = time.Now()
					mu.Unlock()
					return nil
				},
			})
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for /healthz — at this point, Step 4.6 (lifecycle.Start) has already
	// completed because it runs before Step 7 (HTTP server).
	waitForIntegrationHealthy(t, addr)

	// Assert that OnStart was called before healthz became ready.
	mu.Lock()
	startedAtSnapshot := startedAt
	onStartCalledSnapshot := onStartCalled
	mu.Unlock()

	assert.True(t, onStartCalledSnapshot, "OnStart must be called before /healthz is ready")
	assert.False(t, startedAtSnapshot.IsZero(), "OnStart must have recorded a non-zero timestamp")

	// Trigger graceful shutdown.
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("b.Run did not return after cancel")
	}

	// Assert OnStop was called after shutdown.
	mu.Lock()
	stoppedAtSnapshot := stoppedAt
	mu.Unlock()

	assert.False(t, stoppedAtSnapshot.IsZero(), "OnStop must have been called after shutdown")
	assert.True(t, stoppedAtSnapshot.After(startedAtSnapshot) || stoppedAtSnapshot.Equal(startedAtSnapshot),
		"OnStop timestamp must not precede OnStart timestamp")
}

// TestLifecycleIntegration_HookPartialFailure_PreciseRollback verifies the
// LIFO rollback semantics when Hook B's OnStart fails: only Hook A's OnStop
// is called; Hook C's OnStart and OnStop must never execute.
//
// Expected order: ["A.start", "A.stop"]
func TestLifecycleIntegration_HookPartialFailure_PreciseRollback(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	boomErr := errors.New("boom")

	b := bootstrap.New(
		bootstrap.WithShutdownTimeout(3*time.Second),
		bootstrap.WithLifecycle(func(lc bootstrap.Lifecycle) {
			// Hook A: succeeds; its OnStop must run during rollback.
			_ = lc.Append(bootstrap.Hook{
				Name: "A",
				OnStart: func(_ context.Context) error {
					record("A.start")
					return nil
				},
				OnStop: func(_ context.Context) error {
					record("A.stop")
					return nil
				},
			})
			// Hook B: OnStart fails — triggers LIFO rollback of A.
			// B.OnStop must NOT be called (B never completed OnStart).
			_ = lc.Append(bootstrap.Hook{
				Name: "B",
				OnStart: func(_ context.Context) error {
					return boomErr
				},
				OnStop: func(_ context.Context) error {
					t.Error("B.OnStop must not run: B.OnStart failed")
					return nil
				},
			})
			// Hook C: must never run at all (B failed before C was reached).
			_ = lc.Append(bootstrap.Hook{
				Name: "C",
				OnStart: func(_ context.Context) error {
					t.Error("C.OnStart must not run")
					return nil
				},
				OnStop: func(_ context.Context) error {
					t.Error("C.OnStop must not run")
					return nil
				},
			})
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err, "b.Run must return an error when a hook fails")
	assert.True(t, errors.Is(err, boomErr),
		"returned error must wrap boomErr; got: %v", err)

	// Snapshot the order slice after Run returns (no concurrent writes at this point).
	mu.Lock()
	finalOrder := make([]string, len(order))
	copy(finalOrder, order)
	mu.Unlock()

	want := []string{"A.start", "A.stop"}
	assert.Equal(t, want, finalOrder,
		"expected exact LIFO rollback order %v; got %v", want, finalOrder)
}
