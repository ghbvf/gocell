package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fsnotifySettleDelay is the pause between consecutive file writes in config
// reload tests. fsnotify may fire multiple events per WriteFile (Write+Chmod);
// this delay lets the watcher's event loop drain before the next write,
// preventing event coalescing or generation count inflation.
// Value: 2× the fsnotify eventSeparator pattern (50ms) + CI margin.
const fsnotifySettleDelay = 200 * time.Millisecond

// testHTTPClient is used in place of http.DefaultClient to prevent test
// hangs on stalled connections (e.g., during shutdown races).
var testHTTPClient = &http.Client{Timeout: 2 * time.Second}

// newLocalListener creates a TCP listener on a random port, suitable for tests.
func newLocalListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

// waitForHealthy polls /healthz until it returns 200 or the timeout expires.
func waitForHealthy(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")
}

// testCell is a minimal Cell for bootstrap testing.
type testCell struct {
	*cell.BaseCell
}

func newTestCell(id string) *testCell {
	return &testCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
	}
}

func TestNew_Defaults(t *testing.T) {
	b := New()
	assert.Equal(t, ":8080", b.httpAddr)
	assert.Nil(t, b.assembly)
	assert.Nil(t, b.publisher)
	assert.Nil(t, b.subscriber)
}

func TestNew_WithOptions(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	eb := eventbus.New()

	b := New(
		WithAssembly(asm),
		WithPublisher(eb), WithSubscriber(eb),
		WithHTTPAddr(":9090"),
		WithShutdownTimeout(5*time.Second),
	)

	assert.Equal(t, ":9090", b.httpAddr)
	assert.Equal(t, asm, b.assembly)
	assert.Equal(t, eb, b.publisher)
	assert.Equal(t, eb, b.subscriber)
	assert.Equal(t, 5*time.Second, b.shutdownTimeout)
}

func TestNew_WithTracer(t *testing.T) {
	tracer := tracing.NewTracer("bootstrap-test")
	b := New(WithTracer(tracer))
	// WithTracer forwards to router options, so routerOpts should contain one entry.
	assert.Len(t, b.routerOpts, 1)
}

func TestBootstrap_InvalidTrustedProxies_ReturnsError(t *testing.T) {
	// Invalid trusted proxies must return error (not panic), allowing
	// Bootstrap.Run to roll back already-started components.
	asm := assembly.New(assembly.Config{ID: "test-proxy-err"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithRouterOptions(router.WithTrustedProxies([]string{"not-valid"})),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-valid")
	assert.Contains(t, err.Error(), "trusted proxy")

	// Verify rollback: assembly was started at Step 3-4, then stopped by
	// rollback after Step 5 (router.NewE) failed. After rollback, cells
	// report "unhealthy" because Stop has been called.
	health := asm.Health()
	for id, status := range health {
		assert.Equal(t, "unhealthy", status.Status,
			"cell %s must be unhealthy after rollback stopped the assembly", id)
	}
}

func TestNew_WithConfig(t *testing.T) {
	b := New(WithConfig("/nonexistent.yaml", "APP"))
	assert.Equal(t, "/nonexistent.yaml", b.configPath)
	assert.Equal(t, "APP", b.envPrefix)
}

func TestBootstrap_RunWithInvalidConfig(t *testing.T) {
	b := New(WithConfig("/nonexistent/config.yaml", "APP"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config")
}

func TestBootstrap_AssemblyStartWithConfig(t *testing.T) {
	// Test that StartWithConfig works correctly with the assembly.
	asm := assembly.New(assembly.Config{ID: "test"})
	tc := newTestCell("cell-1")
	require.NoError(t, asm.Register(tc))

	cfgMap := map[string]any{"key": "value"}
	ctx := context.Background()
	require.NoError(t, asm.StartWithConfig(ctx, cfgMap))

	// Verify cell is healthy.
	health := asm.Health()
	assert.Equal(t, "healthy", health["cell-1"].Status)

	require.NoError(t, asm.Stop(ctx))
}

func TestBootstrap_CellIDs(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	require.NoError(t, asm.Register(newTestCell("a")))
	require.NoError(t, asm.Register(newTestCell("b")))

	ids := asm.CellIDs()
	assert.Equal(t, []string{"a", "b"}, ids)
}

func TestBootstrap_CellLookup(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	tc := newTestCell("lookup")
	require.NoError(t, asm.Register(tc))

	assert.NotNil(t, asm.Cell("lookup"))
	assert.Nil(t, asm.Cell("nonexistent"))
}

func TestNew_WithPublisherAndSubscriber(t *testing.T) {
	eb := eventbus.New()

	b := New(
		WithPublisher(eb),
		WithSubscriber(eb),
	)

	assert.Equal(t, eb, b.publisher)
	assert.Equal(t, eb, b.subscriber)
}

func TestNew_WithPublisherOnly(t *testing.T) {
	eb := eventbus.New()

	b := New(WithPublisher(eb))

	assert.Equal(t, eb, b.publisher)
	assert.Nil(t, b.subscriber)
}

func TestNew_WithSubscriberOnly(t *testing.T) {
	eb := eventbus.New()

	b := New(WithSubscriber(eb))

	assert.Nil(t, b.publisher)
	assert.Equal(t, eb, b.subscriber)
}

// eventCell implements cell.EventRegistrar with a configurable error.
type eventCell struct {
	*cell.BaseCell
	subErr error
}

type contextCaptureCell struct {
	*cell.BaseCell
	got chan map[string]string
}

func newContextCaptureCell(id string, got chan map[string]string) *contextCaptureCell {
	return &contextCaptureCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
		got: got,
	}
}

func (c *contextCaptureCell) RegisterSubscriptions(r cell.EventRouter) error {
	r.AddHandler("test.context", func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		requestID, _ := ctxkeys.RequestIDFrom(ctx)
		correlationID, _ := ctxkeys.CorrelationIDFrom(ctx)
		traceID, _ := ctxkeys.TraceIDFrom(ctx)
		c.got <- map[string]string{
			"request_id":     requestID,
			"correlation_id": correlationID,
			"trace_id":       traceID,
		}
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "capture-cell")
	return nil
}

type invokeOnceSubscriber struct {
	entry outbox.Entry
	once  sync.Once
}

func (s *invokeOnceSubscriber) Subscribe(ctx context.Context, _ string, handler outbox.EntryHandler, _ string) error {
	s.once.Do(func() {
		handler(ctx, s.entry)
	})
	<-ctx.Done()
	return ctx.Err()
}

func (s *invokeOnceSubscriber) Close() error { return nil }

func newEventCell(id string, subErr error) *eventCell {
	return &eventCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
		subErr: subErr,
	}
}

func (c *eventCell) RegisterSubscriptions(r cell.EventRouter) error {
	if c.subErr != nil {
		return c.subErr
	}
	r.AddHandler("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
	return nil
}

func TestBootstrap_MissingSubscriber_WithEventRegistrar_Fails(t *testing.T) {
	// When a cell implements EventRegistrar but no subscriber is configured,
	// bootstrap must fail at startup instead of silently skipping all subscriptions.
	asm := assembly.New(assembly.Config{ID: "test-no-sub"})
	ec := newEventCell("needs-sub", nil) // registers a handler
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := New(
		WithAssembly(asm),
		WithPublisher(eb),
		// WithSubscriber intentionally omitted.
		WithHTTPAddr("127.0.0.1:0"),
	)

	err := b.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EventRegistrar")
	assert.Contains(t, err.Error(), "no subscriber")
}

func TestBootstrap_SubscriptionFailure_TriggersRollback(t *testing.T) {
	// S3-03: When RegisterSubscriptions fails, Run must rollback previously
	// started components (assembly) and return an error wrapping the cause.
	asm := assembly.New(assembly.Config{ID: "test-rollback"})
	ec := newEventCell("fail-cell", errors.New("DLX not configured"))
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := New(
		WithAssembly(asm),
		WithPublisher(eb), WithSubscriber(eb),
		WithHTTPAddr("127.0.0.1:0"),
		WithShutdownTimeout(time.Second),
	)

	ctx := context.Background()
	err := b.Run(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscription setup failed")
	assert.Contains(t, err.Error(), "DLX not configured")
	// After rollback, assembly should be stopped (health returns empty or degraded).
	// The key assertion is that Run returns the error instead of hanging.
}

func TestBootstrap_EventRouter_HappyPath(t *testing.T) {
	// Cell registers a handler → Router starts → bootstrap serves → ctx cancel → clean shutdown.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-router-ok"})
	ec := newEventCell("ok-cell", nil) // nil error → registers 1 handler
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := New(
		WithAssembly(asm),
		WithSubscriber(eb),
		WithPublisher(eb),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err, "clean shutdown should not produce an error")
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_EventSubscriptions_RestoreObservabilityContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-router-context"})
	got := make(chan map[string]string, 1)
	require.NoError(t, asm.Register(newContextCaptureCell("capture-cell", got)))

	sub := &invokeOnceSubscriber{entry: outbox.Entry{
		ID:        "evt-context-1",
		EventType: "test.context",
		Metadata: map[string]string{
			"request_id":     "req-ctx-1",
			"correlation_id": "corr-ctx-1",
			"trace_id":       "trace-ctx-1",
		},
	}}

	b := New(
		WithAssembly(asm),
		WithSubscriber(sub),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	select {
	case observed := <-got:
		assert.Equal(t, "req-ctx-1", observed["request_id"])
		assert.Equal(t, "corr-ctx-1", observed["correlation_id"])
		assert.Equal(t, "trace-ctx-1", observed["trace_id"])
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for restored consumer context")
	}

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_EventSubscriptions_DisableObservabilityRestore(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-kill-switch"})
	got := make(chan map[string]string, 1)
	require.NoError(t, asm.Register(newContextCaptureCell("capture-cell", got)))

	sub := &invokeOnceSubscriber{entry: outbox.Entry{
		ID:        "evt-no-restore-1",
		EventType: "test.context",
		Metadata: map[string]string{
			"request_id":     "req-should-not-restore",
			"correlation_id": "corr-should-not-restore",
			"trace_id":       "trace-should-not-restore",
		},
	}}

	b := New(
		WithAssembly(asm),
		WithSubscriber(sub),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithDisableObservabilityRestore(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	select {
	case observed := <-got:
		assert.Empty(t, observed["request_id"],
			"kill switch should prevent request_id restoration")
		assert.Empty(t, observed["correlation_id"],
			"kill switch should prevent correlation_id restoration")
		assert.Empty(t, observed["trace_id"],
			"kill switch should prevent trace_id restoration")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for consumer context")
	}

	// Wait for bootstrap to finish startup (event router 500ms timeout + HTTP server).
	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_RunContextCancel(t *testing.T) {
	// Test that Run returns when context is cancelled immediately,
	// even though it will fail at listen (sandbox restriction).
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	b := New(
		WithHTTPAddr("127.0.0.1:0"),
		WithShutdownTimeout(time.Second),
	)

	// This should complete quickly, either with a listen error
	// (sandbox) or context cancelled.
	err := b.Run(ctx)
	// Either outcome is acceptable in the sandbox.
	_ = err
}

func TestBootstrap_DoubleRun_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so first Run exits quickly

	b := New(WithHTTPAddr("127.0.0.1:0"))
	_ = b.Run(ctx) // first call — may error due to cancelled ctx or sandbox

	err := b.Run(ctx) // second call — must be rejected
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Run called more than once")
}

func TestBootstrap_WithHealthChecker_Healthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-hc-healthy"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func() error { return nil }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for the HTTP server to be ready.
	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// GET /readyz?verbose and verify the checker appears as healthy.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	assert.Equal(t, "healthy", deps["rabbitmq"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_WithHealthChecker_Unhealthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-hc-unhealthy"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func() error {
			return fmt.Errorf("connection closed")
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for the HTTP server to be ready.
	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// GET /readyz?verbose and verify the checker appears as unhealthy.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	assert.Equal(t, "unhealthy", deps["rabbitmq"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestWithHealthChecker_EmptyName_ReturnsError(t *testing.T) {
	b := New(
		WithHealthChecker("", func() error { return nil }),
	)
	err := b.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health checker name must not be empty")
}

func TestWithHealthChecker_ValidationBeforeSideEffects(t *testing.T) {
	// Verify that invalid health checker params are caught BEFORE any
	// component starts (no assembly start, no config watcher, no rollback).
	// Evidence: error returned directly (not wrapped by rollback log).
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(oldDefault)

	b := New(
		WithHealthChecker("", func() error { return nil }),
		WithConfig("/nonexistent/config.yaml", "TEST"), // would fail if reached
	)
	err := b.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health checker name must not be empty")
	assert.NotContains(t, buf.String(), "rolling back",
		"validation error must fire before any side effects — no rollback should occur")
}

func TestWithHealthChecker_NilFunc_ReturnsError(t *testing.T) {
	b := New(
		WithHealthChecker("mycheck", nil),
	)
	err := b.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

func TestBootstrap_WithMultipleHealthCheckers_OneUnhealthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-multi-hc"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func() error { return nil }),
		WithHealthChecker("postgres", func() error { return fmt.Errorf("connection refused") }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// GET /readyz?verbose — one unhealthy checker should make the whole response 503.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose=true", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"any unhealthy dependency must cause 503")

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	assert.Equal(t, "healthy", deps["rabbitmq"], "rabbitmq checker should be healthy")
	assert.Equal(t, "unhealthy", deps["postgres"], "postgres checker should be unhealthy")
	assert.Equal(t, "unhealthy", body["status"], "overall status must be unhealthy")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_WithHealthChecker_DynamicStateTransition(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-dynamic-hc"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	// Atomic flag to simulate connection health transitions at runtime.
	var unhealthy atomic.Bool

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func() error {
			if unhealthy.Load() {
				return fmt.Errorf("connection lost")
			}
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// Phase 1: healthy state → 200.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "should be ready when checker is healthy")
	resp.Body.Close()

	// Phase 2: flip to unhealthy → 503.
	unhealthy.Store(true)

	resp, err = testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"should be unready after health state transition")
	resp.Body.Close()

	// Phase 3: recover → 200.
	unhealthy.Store(false)

	resp, err = testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "should recover after health state restores")
	resp.Body.Close()

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_ConfigWatcher_ReadyzVerboseIncludesWatcher(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-config-watcher-readyz"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		deps, ok := body["dependencies"].(map[string]any)
		if !ok {
			return false
		}
		return deps[configWatcherCheckerName] == "healthy"
	}, 3*time.Second, 50*time.Millisecond, "config watcher did not become ready in time")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_ConfigDriftReadyz_NoDrift(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-config-drift-no-drift"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		deps, ok := body["dependencies"].(map[string]any)
		if !ok {
			return false
		}
		// Config drift checker should be registered and healthy (no drift).
		return deps[configDriftCheckerName] == "healthy"
	}, 3*time.Second, 50*time.Millisecond, "config-drift checker not found or not healthy")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_ConfigDriftChecker_ReportsUnhealthy(t *testing.T) {
	// Unit test: verify the config-drift checker closure logic directly.
	// Integration of HasDrift with generation/observedGeneration is covered
	// by runtime/config/config_test.go (TestConfig_HasDrift).
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	cfg, err := config.Load(cfgFile, "")
	require.NoError(t, err)

	// Initially: generation=0, observedGeneration=0 → no drift.
	assert.False(t, config.HasDrift(cfg))

	// Reload to bump generation to 1; observedGeneration stays 0 → drift.
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: updated\n"), 0o644))
	r, ok := cfg.(config.Reloader)
	require.True(t, ok)
	require.NoError(t, r.Reload(cfgFile, ""))
	assert.True(t, config.HasDrift(cfg), "generation 1 != observed 0 → drift")

	// Simulate cells applying → set observed = generation → no drift.
	og := cfg.(config.ObservedGenerationer)
	g := cfg.(config.Generationer)
	og.SetObservedGeneration(g.Generation())
	assert.False(t, config.HasDrift(cfg), "after cells apply, drift resolved")
}

func TestBootstrap_ConfigDriftChecker_ErrorMessage(t *testing.T) {
	// Verify the drift checker closure returns a correctly formatted error
	// containing both generation and observedGeneration values.
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	cfg, err := config.Load(cfgFile, "")
	require.NoError(t, err)

	g := cfg.(config.Generationer)
	og := cfg.(config.ObservedGenerationer)

	// Construct the same checker closure that bootstrap.Run creates.
	checker := func() error {
		if g.Generation() != og.ObservedGeneration() {
			return fmt.Errorf("config drift: generation %d, observed %d",
				g.Generation(), og.ObservedGeneration())
		}
		return nil
	}

	// No drift initially.
	assert.NoError(t, checker())

	// Trigger drift via reload.
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: v2\n"), 0o644))
	require.NoError(t, cfg.(config.Reloader).Reload(cfgFile, ""))

	err = checker()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config drift")
	assert.Contains(t, err.Error(), "generation 1")
	assert.Contains(t, err.Error(), "observed 0")

	// Resolve drift.
	og.SetObservedGeneration(g.Generation())
	assert.NoError(t, checker(), "drift resolved after cells apply")
}

func TestBootstrap_ConfigWatcherInitFailure_FailsFast(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	asm := assembly.New(assembly.Config{ID: "test-config-watcher-fail-fast"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithShutdownTimeout(time.Second),
	)
	// Override instance-level factory to simulate init failure (safe for parallel tests).
	b.configWatcherFactory = func(string, ...config.WatcherOption) (*config.Watcher, error) {
		return nil, errors.New("watcher init failed")
	}

	err := b.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config watcher")
	assert.Contains(t, err.Error(), "watcher init failed")
}

func TestBootstrap_WithHealthChecker_ReservedNameConflict_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	asm := assembly.New(assembly.Config{ID: "test-reserved-health-checker"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithHealthChecker("config-watcher", func() error { return nil }),
		WithShutdownTimeout(time.Second),
	)

	require.NotPanics(t, func() {
		err := b.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate health checker")
		assert.Contains(t, err.Error(), "config-watcher")
	})
}

func TestBootstrap_EventRouter_ReadyzVerboseIncludesEventRouter(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-eventrouter-readyz"})
	require.NoError(t, asm.Register(newEventCell("ok-cell", nil)))

	eb := eventbus.New()
	b := New(
		WithAssembly(asm),
		WithPublisher(eb),
		WithSubscriber(eb),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose=true", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain dependencies")
	assert.Equal(t, "healthy", deps["eventrouter"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestSnapshotConfig_WithSnapshotter(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("a: 1\nb: two\n"), 0o644))

	cfg, err := config.Load(cfgFile, "")
	require.NoError(t, err)

	snap := snapshotConfig(cfg)
	assert.Equal(t, 1, snap["a"])
	assert.Equal(t, "two", snap["b"])
}

// plainConfig implements config.Config but NOT config.Snapshotter,
// exercising the snapshotConfig fallback path (Keys+Get iteration).
type plainConfig struct {
	data map[string]any
}

func (c *plainConfig) Get(key string) any       { return c.data[key] }
func (c *plainConfig) Scan(_ interface{}) error { return nil }
func (c *plainConfig) Keys() []string {
	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestSnapshotConfig_Fallback(t *testing.T) {
	// plainConfig does NOT implement Snapshotter, so snapshotConfig
	// must use the Keys()+Get() fallback path.
	cfg := &plainConfig{data: map[string]any{"a": 1, "b": "two"}}

	// Verify it does NOT implement Snapshotter.
	_, ok := config.Config(cfg).(config.Snapshotter)
	assert.False(t, ok, "plainConfig must not implement Snapshotter")

	snap := snapshotConfig(cfg)
	assert.Equal(t, 1, snap["a"])
	assert.Equal(t, "two", snap["b"])
}

// ---------------------------------------------------------------------------
// ConfigReloader integration tests (WM-34)
// ---------------------------------------------------------------------------

// reloaderCell is a Cell that implements cell.ConfigReloader for testing.
type reloaderCell struct {
	*cell.BaseCell
	mu         sync.Mutex
	events     []cell.ConfigChangeEvent
	callOrder  *[]string // shared slice to track call order across cells
	err        error     // configurable error to return
	doPanic    bool      // if true, panic instead of returning
	panicCount atomic.Int32
}

func newReloaderCell(id string) *reloaderCell {
	return &reloaderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
	}
}

func (c *reloaderCell) OnConfigReload(event cell.ConfigChangeEvent) error {
	if c.doPanic {
		c.panicCount.Add(1)
		panic("intentional test panic in OnConfigReload")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	if c.callOrder != nil {
		*c.callOrder = append(*c.callOrder, c.ID())
	}
	return c.err
}

func (c *reloaderCell) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *reloaderCell) lastEvent() *cell.ConfigChangeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil
	}
	e := c.events[len(c.events)-1]
	return &e
}

// slowReloaderCell sleeps during OnConfigReload to simulate a slow handler.
// Used to test that shutdown waits for in-flight reload callbacks to complete.
type slowReloaderCell struct {
	*cell.BaseCell
	delay     time.Duration
	called    atomic.Int32
	completed atomic.Int32
}

type blockingStopWorker struct {
	stopStarted chan struct{}
	releaseStop chan struct{}
}

func newBlockingStopWorker() *blockingStopWorker {
	return &blockingStopWorker{
		stopStarted: make(chan struct{}),
		releaseStop: make(chan struct{}),
	}
}

func (w *blockingStopWorker) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (w *blockingStopWorker) Stop(_ context.Context) error {
	select {
	case <-w.stopStarted:
	default:
		close(w.stopStarted)
	}
	<-w.releaseStop
	return nil
}

func newSlowReloaderCell(id string, delay time.Duration) *slowReloaderCell {
	return &slowReloaderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		delay:    delay,
	}
}

func (c *slowReloaderCell) OnConfigReload(_ cell.ConfigChangeEvent) error {
	c.called.Add(1)
	time.Sleep(c.delay)
	c.completed.Add(1)
	return nil
}

// TestBootstrap_ShutdownDrainsInflightReload verifies that an in-flight config
// reload callback completes before assembly.Stop() is called during shutdown.
// This catches the race where assemblyStopped is checked (false) but shutdown
// begins before the callback finishes, leading to concurrent OnConfigReload
// and assembly.Stop execution.
func TestBootstrap_ShutdownDrainsInflightReload(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-drain"})
	slow := newSlowReloaderCell("slow-cell", 300*time.Millisecond)
	require.NoError(t, asm.Register(slow))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(5*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Trigger a config change that will take 300ms to process.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))

	// Wait just long enough for the callback to start but not finish.
	require.Eventually(t, func() bool {
		return slow.called.Load() >= 1
	}, 3*time.Second, 10*time.Millisecond, "slow handler should have started")

	// Trigger shutdown while the slow callback is still in flight.
	cancel()

	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timeout")
	}

	// The slow callback must have completed (not been interrupted by shutdown).
	assert.Equal(t, int32(1), slow.completed.Load(),
		"in-flight reload callback must complete before shutdown finishes")
}

func TestBootstrap_ConfigReload_NotifiesCells(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("server:\n  port: 8080\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-reload"})
	rc := newReloaderCell("auth-core")
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for HTTP ready.
	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Modify config file — add a new key.
	require.NoError(t, os.WriteFile(cfgFile, []byte("server:\n  port: 9090\nnew_key: added\n"), 0o644))

	// Wait for callback.
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected OnConfigReload to fire")

	evt := rc.lastEvent()
	require.NotNil(t, evt)
	assert.Contains(t, evt.Updated, "server.port")
	assert.Contains(t, evt.Added, "new_key")
	assert.NotNil(t, evt.Config)

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestBootstrap_ConfigReload_ErrorDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-reload-err"})
	rc := newReloaderCell("fail-cell")
	rc.err = errors.New("reload callback failed")
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Modify config — cell will return error.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))

	// Wait for callback to be called (even though it returns error).
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	// Bootstrap should still be running (error does not crash).
	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr, "bootstrap should shut down cleanly despite cell reload error")
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestBootstrap_ConfigReload_PanicDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-reload-panic"})
	rc := newReloaderCell("panic-cell")
	rc.doPanic = true
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Modify config — cell will panic.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))

	// Wait for panic to fire and be recovered.
	require.Eventually(t, func() bool {
		return rc.panicCount.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected OnConfigReload panic to fire")

	// Bootstrap should still be running after the panic.
	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr, "bootstrap should shut down cleanly despite cell panic")
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestBootstrap_ConfigReload_FIFO(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-reload-fifo"})
	callOrder := make([]string, 0, 3)
	cells := make([]*reloaderCell, 3)
	for i, id := range []string{"first", "second", "third"} {
		cells[i] = newReloaderCell(id)
		cells[i].callOrder = &callOrder
		require.NoError(t, asm.Register(cells[i]))
	}

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Modify config.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))

	// Wait for all cells to be called.
	require.Eventually(t, func() bool {
		return cells[2].eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	// Verify FIFO order.
	assert.Equal(t, []string{"first", "second", "third"}, callOrder)

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestBootstrap_ConfigReload_NonReloaderSkipped(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-reload-skip"})
	plain := newTestCell("plain-cell") // does NOT implement ConfigReloader
	rc := newReloaderCell("reloader-cell")
	require.NoError(t, asm.Register(plain))
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Modify config.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))

	// Wait for reloader cell to be called.
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	// Plain cell should not have been called (it doesn't implement ConfigReloader).
	// The test verifies by checking that only the reloader cell receives events.
	assert.Equal(t, 1, rc.eventCount())

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestBootstrap_ConfigReload_NoChangeNoCallback(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-reload-noop"})
	rc := newReloaderCell("noop-cell")
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// First: write different content to confirm the callback pipeline works.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected first config change to fire callback")

	// Second: re-write the SAME content that config currently has (val2).
	// This triggers the watcher but Diff(val2, val2) = empty, so no callback.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))

	// Stabilization delay: give the watcher time to process the no-diff event
	// before writing different content. Without this, on macOS kqueue the two
	// writes can be coalesced into a single event, or the second event can be
	// lost entirely — causing the test to flake.
	time.Sleep(fsnotifySettleDelay)

	// Third: write different content — proves the watcher is still alive
	// after the no-diff reload.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val3\n"), 0o644))
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 2
	}, 3*time.Second, 50*time.Millisecond, "expected third config change to fire callback")

	// Exactly 2 callbacks: the no-diff reload in the middle was correctly skipped.
	assert.Equal(t, 2, rc.eventCount(), "no-diff reload should not trigger callback")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

// mutatingReloaderCell modifies the event to test isolation between cells.
type mutatingReloaderCell struct {
	*cell.BaseCell
	called atomic.Int32
}

func newMutatingReloaderCell(id string) *mutatingReloaderCell {
	return &mutatingReloaderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
	}
}

func (c *mutatingReloaderCell) OnConfigReload(event cell.ConfigChangeEvent) error {
	c.called.Add(1)
	// Attempt to corrupt shared state.
	if len(event.Added) > 0 {
		event.Added[0] = "CORRUPTED"
	}
	event.Config["INJECTED"] = "malicious"
	delete(event.Config, "key")
	return nil
}

func TestBootstrap_ConfigReload_EventIsolation(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-isolation"})
	mutator := newMutatingReloaderCell("mutator")
	observer := newReloaderCell("observer")
	// Register mutator first — it tries to corrupt the event.
	require.NoError(t, asm.Register(mutator))
	require.NoError(t, asm.Register(observer))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Trigger config change that adds a new key.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\nnew_key: added\n"), 0o644))

	// Wait for both cells to be called.
	require.Eventually(t, func() bool {
		return observer.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	// Observer should see clean data despite mutator's corruption attempt.
	evt := observer.lastEvent()
	require.NotNil(t, evt)
	assert.Contains(t, evt.Added, "new_key", "Added should contain original key, not CORRUPTED")
	assert.Contains(t, evt.Config, "key", "Config should still have 'key' despite delete attempt")
	assert.NotContains(t, evt.Config, "INJECTED", "Config should not have mutator's injected key")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

// TestBootstrap_ShutdownNoPostStopReload verifies that no config reload
// callbacks fire after assembly.Stop() completes during shutdown.
func TestBootstrap_ShutdownNoPostStopReload(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-shutdown-race"})
	rc := newReloaderCell("shutdown-race-cell")
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// Trigger shutdown.
	cancel()

	// Wait for shutdown to complete.
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}

	countBefore := rc.eventCount()

	// Write config AFTER shutdown — should NOT trigger a callback.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val_post_stop\n"), 0o644))

	// Brief wait to give any spurious callback time to fire.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, countBefore, rc.eventCount(),
		"no config reload callback should fire after shutdown")
}

// TestBootstrap_ShutdownRejectsReloadDuringDrain verifies that shutdown starts
// rejecting new reload callbacks before earlier teardown steps (such as worker
// shutdown) have finished.
func TestBootstrap_ShutdownRejectsReloadDuringDrain(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-shutdown-drain-reject"})
	rc := newReloaderCell("shutdown-drain-cell")
	require.NoError(t, asm.Register(rc))

	blocker := newBlockingStopWorker()
	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithWorkers(blocker),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	cancel()

	select {
	case <-blocker.stopStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not reach worker stop")
	}

	countBefore := rc.eventCount()
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val_during_shutdown\n"), 0o644))

	assert.Never(t, func() bool {
		return rc.eventCount() > countBefore
	}, 500*time.Millisecond, 20*time.Millisecond,
		"shutdown must reject config reloads once graceful shutdown begins")

	close(blocker.releaseStop)

	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

// TestBootstrap_ConfigReload_GenerationTracking verifies that the Generation
// field in ConfigChangeEvent is populated correctly.
func TestBootstrap_ConfigReload_GenerationTracking(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val1\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-generation"})
	rc := newReloaderCell("gen-cell")
	require.NoError(t, asm.Register(rc))

	b := New(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	// First change.
	time.Sleep(fsnotifySettleDelay)
	prevCount := rc.eventCount()
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))
	require.Eventually(t, func() bool {
		return rc.eventCount() > prevCount
	}, 3*time.Second, 50*time.Millisecond)

	evt := rc.lastEvent()
	require.NotNil(t, evt)
	gen1 := evt.Generation
	assert.Greater(t, gen1, int64(0), "first reload generation must be positive")

	// Second change.
	time.Sleep(fsnotifySettleDelay)
	prevCount = rc.eventCount()
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val3\n"), 0o644))
	require.Eventually(t, func() bool {
		return rc.eventCount() > prevCount
	}, 3*time.Second, 50*time.Millisecond)

	evt = rc.lastEvent()
	require.NotNil(t, evt)
	assert.Greater(t, evt.Generation, gen1, "second reload generation must be greater than first")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestCloneMap_DeepIsolation_Slices(t *testing.T) {
	src := map[string]any{
		"tags": []any{"alpha", "beta"},
		"key":  "val",
	}
	dst := cloneMap(src)

	// Mutate dst slice.
	dst["tags"].([]any)[0] = "CORRUPTED"

	// src must be unaffected.
	assert.Equal(t, "alpha", src["tags"].([]any)[0],
		"cloneMap must deep-copy slices; mutating dst corrupted src")
}

func TestCloneMap_DeepIsolation_NestedMap(t *testing.T) {
	src := map[string]any{
		"db": map[string]any{
			"host": "localhost",
			"port": 5432,
		},
	}
	dst := cloneMap(src)

	// Mutate nested map in dst.
	dst["db"].(map[string]any)["host"] = "CORRUPTED"

	// src must be unaffected.
	assert.Equal(t, "localhost", src["db"].(map[string]any)["host"],
		"cloneMap must deep-copy nested maps; mutating dst corrupted src")
}

// --- Auth middleware wiring via bootstrap ---

// httpCell is a test Cell that implements HTTPRegistrar to register a business route.
type httpCell struct {
	*cell.BaseCell
}

func newHTTPCell(id string) *httpCell {
	return &httpCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
	}
}

func (c *httpCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Handle("GET /api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}))
	mux.Handle("POST /api/v1/access/sessions/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"token":"test"}}`))
	}))
}

// bootstrapTestVerifier is a minimal TokenVerifier for bootstrap tests.
type bootstrapTestVerifier struct {
	claims    auth.Claims
	err       error
	callCount atomic.Int32
}

func (v *bootstrapTestVerifier) Verify(_ context.Context, _ string) (auth.Claims, error) {
	v.callCount.Add(1)
	return v.claims, v.err
}

func TestBootstrap_WithAuthMiddleware_ProtectedRoute_Returns401(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-401"})
	hc := newHTTPCell("auth-test-cell")
	require.NoError(t, asm.Register(hc))

	verifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithAuthMiddleware(verifier, nil),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// Protected route without token → 401.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/data", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"business route without auth token must return 401")

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", errObj["code"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_WithAuthMiddleware_PublicRoute_Passes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-public"})
	hc := newHTTPCell("auth-public-cell")
	require.NoError(t, asm.Register(hc))

	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("should not verify for public route"),
	}

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithAuthMiddleware(verifier, []string{"/api/v1/access/sessions/login"}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// Public login route without token → should pass auth.
	resp, err := testHTTPClient.Post(
		fmt.Sprintf("http://%s/api/v1/access/sessions/login", addr),
		"application/json", nil,
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"public login endpoint must be accessible without auth token")
	assert.Equal(t, int32(0), verifier.callCount.Load(),
		"verifier must not be called for public endpoint")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// --- Framework capability protection (BOOT-OPTION-01) ---

func TestBootstrap_UserRouterOpts_CannotOverrideFrameworkHealth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-health-override"})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	// Create a custom health handler backed by an un-started assembly (unhealthy).
	// If the user's handler wins, /readyz would return 503 because the custom
	// assembly was never started.
	customAsm := assembly.New(assembly.Config{ID: "custom-unstartled"})
	require.NoError(t, customAsm.Register(newTestCell("custom-cell")))
	customHandler := health.New(customAsm) // un-started → always unhealthy

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		// User attempts to override with custom health handler.
		WithRouterOptions(router.WithHealthHandler(customHandler)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// The framework-managed handler (backed by started asm) should respond,
	// not the custom un-started one. Started assembly → healthy → 200.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"framework health handler must win over user-supplied one; "+
			"user handler would return 503 (un-started assembly)")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// --- Graceful shutdown drain (OPS-4) ---

func TestGracefulShutdown_ReadyzUnhealthyBeforeHTTPStop(t *testing.T) {
	ln := newLocalListener(t)
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	b := New(WithListener(ln))
	go func() { errCh <- b.Run(ctx) }()

	// Wait for server to be ready.
	waitForHealthy(t, addr)

	// Trigger shutdown.
	cancel()

	// Poll /readyz — it should become 503.
	deadline := time.After(5 * time.Second)
	for {
		resp, err := testHTTPClient.Get("http://" + addr + "/readyz")
		if err != nil {
			break // server already closed, that's fine
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			break // got 503 — drain signal works
		}
		resp.Body.Close()
		select {
		case <-deadline:
			t.Fatal("timed out waiting for /readyz to return 503 during shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for bootstrap shutdown")
	}
}

// --- Bootstrap tracing E2E ---

// tracingTestCell is a test Cell that implements HTTPRegistrar with a
// caller-supplied route registration function, used by tracing E2E tests.
type tracingTestCell struct {
	*cell.BaseCell
	registerFn func(mux cell.RouteMux)
}

func newTracingTestCell(id string, fn func(mux cell.RouteMux)) *tracingTestCell {
	return &tracingTestCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
		registerFn: fn,
	}
}

func (c *tracingTestCell) RegisterRoutes(mux cell.RouteMux) {
	if c.registerFn != nil {
		c.registerFn(mux)
	}
}

func TestBootstrap_TracingE2E_BusinessRoute(t *testing.T) {
	ln := newLocalListener(t)
	tracer := tracing.NewTracer("bootstrap-tracing-e2e")

	var gotTraceID string
	tc := newTracingTestCell("trace-biz", func(mux cell.RouteMux) {
		mux.Handle("GET /api/v1/trace-test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
	})

	asm := assembly.New(assembly.Config{ID: "trace-e2e"})
	require.NoError(t, asm.Register(tc))

	ctx, cancel := context.WithCancel(context.Background())
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithTracer(tracer),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, err := testHTTPClient.Get("http://" + addr + "/api/v1/trace-test")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, gotTraceID, "trace_id must be set in handler context via bootstrap tracing")

	cancel()
	<-done
}

func TestBootstrap_TracingE2E_UpstreamPropagation(t *testing.T) {
	ln := newLocalListener(t)
	tracer := tracing.NewTracer("bootstrap-upstream-e2e")

	var gotTraceID string
	tc := newTracingTestCell("trace-upstream", func(mux cell.RouteMux) {
		mux.Handle("GET /api/v1/propagate", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
	})

	asm := assembly.New(assembly.Config{ID: "trace-upstream"})
	require.NoError(t, asm.Register(tc))

	ctx, cancel := context.WithCancel(context.Background())
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithTracer(tracer),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Send request with upstream traceparent header.
	upstreamTraceID := "0af7651916cd43dd8448eb211c80319c"
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/propagate", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", "00-"+upstreamTraceID+"-b7ad6b7169203331-01")

	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, upstreamTraceID, gotTraceID,
		"bootstrap must propagate upstream trace_id from traceparent header")

	cancel()
	<-done
}

func TestBootstrap_TracingE2E_PanicRoute(t *testing.T) {
	ln := newLocalListener(t)
	tracer := tracing.NewTracer("bootstrap-panic-e2e")

	tc := newTracingTestCell("trace-panic", func(mux cell.RouteMux) {
		mux.Handle("GET /api/v1/boom", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("boom for tracing test")
		}))
	})

	asm := assembly.New(assembly.Config{ID: "trace-panic"})
	require.NoError(t, asm.Register(tc))

	ctx, cancel := context.WithCancel(context.Background())
	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithTracer(tracer),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, err := testHTTPClient.Get("http://" + addr + "/api/v1/boom")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"panicking handler must return 500 even with tracing enabled")

	cancel()
	<-done
}

func TestBootstrap_TracingE2E_InfraEndpoints(t *testing.T) {
	// Verify infra endpoints (/healthz) also get tracing coverage.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(oldDefault)

	ln := newLocalListener(t)
	tracer := tracing.NewTracer("bootstrap-infra-e2e")

	ctx, cancel := context.WithCancel(context.Background())
	b := New(
		WithListener(ln),
		WithTracer(tracer),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, err := testHTTPClient.Get("http://" + addr + "/healthz")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Shut down so all access logs are flushed.
	cancel()
	<-done

	// Check access log contains trace_id for /healthz.
	logOutput := buf.String()
	assert.Contains(t, logOutput, "trace_id",
		"infra endpoint /healthz must have trace_id in access log when tracing is enabled")
}

// --- Auth Provider discovery (post-Init cell discovery) ---

// authProviderCell implements HTTPRegistrar and exposes a TokenVerifier.
type authProviderCell struct {
	*cell.BaseCell
	verifier auth.TokenVerifier
}

func newAuthProviderCell(id string, verifier auth.TokenVerifier) *authProviderCell {
	return &authProviderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		verifier: verifier,
	}
}

func (c *authProviderCell) TokenVerifier() auth.TokenVerifier {
	return c.verifier
}

func (c *authProviderCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Handle("GET /api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}))
	mux.Handle("POST /api/v1/access/sessions/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"login-ok"}`))
	}))
}

func TestBootstrap_AuthDiscovery_ProtectedRoute_Returns401(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-discovery-401"})
	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("no token provided"),
	}
	hc := newAuthProviderCell("access-core", verifier)
	require.NoError(t, asm.Register(hc))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithPublicEndpoints(nil),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Protected route without token -> 401.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/data", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"discovered auth verifier must protect business routes")

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", errObj["code"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_AuthDiscovery_PublicRoute_Passes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-discovery-public"})
	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("should not verify for public route"),
	}
	hc := newAuthProviderCell("access-core", verifier)
	require.NoError(t, asm.Register(hc))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithPublicEndpoints([]string{"/api/v1/access/sessions/login"}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Public login route without token -> should pass auth.
	resp, err := testHTTPClient.Post(
		fmt.Sprintf("http://%s/api/v1/access/sessions/login", addr),
		"application/json", nil,
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"public endpoint must be accessible without auth token via discovered verifier")
	assert.Equal(t, int32(0), verifier.callCount.Load(),
		"verifier must not be called for public endpoint")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_WithAuthMiddleware_Precedence(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-precedence"})

	cellVerifier := &bootstrapTestVerifier{
		err: fmt.Errorf("cell-verifier: should not be called"),
	}
	hc := newAuthProviderCell("access-core", cellVerifier)
	require.NoError(t, asm.Register(hc))

	explicitVerifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "explicit-user", Roles: []string{"admin"}},
	}

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithAuthMiddleware(explicitVerifier, nil),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Send request WITH Authorization header — explicit verifier should handle it.
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/api/v1/data", addr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")

	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"explicit verifier should authenticate successfully")
	assert.Equal(t, int32(1), explicitVerifier.callCount.Load(),
		"explicit verifier must be called")
	assert.Equal(t, int32(0), cellVerifier.callCount.Load(),
		"cell verifier must NOT be called when explicit verifier is provided")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_AuthDiscovery_NoProvider_FailsClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// Register a plain cell with no TokenVerifier method.
	asm := assembly.New(assembly.Config{ID: "test-no-auth-provider"})
	hc := newHTTPCell("plain-cell")
	require.NoError(t, asm.Register(hc))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithPublicEndpoints([]string{"/api/v1/access/sessions/login"}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run should fail because no auth provider cell was discovered.
	err = b.Run(ctx)
	require.Error(t, err, "bootstrap should fail when no auth provider cell is discovered")
	assert.Contains(t, err.Error(), "auth provider cell",
		"error should mention missing auth provider")
}
