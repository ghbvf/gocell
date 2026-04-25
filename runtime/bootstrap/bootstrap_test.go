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
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fsnotifySettleDelay is the pause between consecutive file writes in config
// reload tests. fsnotify may fire multiple events per WriteFile (Write+Chmod);
// this delay lets the watcher's event loop drain before the next write,
// preventing event coalescing or generation count inflation.
// Value: 2× the fsnotify eventSeparator pattern (50ms) + CI margin.
const fsnotifySettleDelay = 200 * time.Millisecond

// testVerboseToken is the canonical token wired by test bootstraps via
// WithVerboseToken so that /readyz?verbose responses are served to
// assertions. PR-A35 removed the prior "no token = open verbose" path:
// every verbose request must now carry a matching X-Readyz-Token header.
const testVerboseToken = "bootstrap-test-verbose"

// autoVerboseTokenTransport injects X-Readyz-Token on every outbound
// request so tests do not have to thread the header through each GET call.
// Tests that specifically want to exercise token failure paths must
// construct their own http.Client (or use http.DefaultClient) and send
// requests without this transport.
type autoVerboseTokenTransport struct{ base http.RoundTripper }

func (t *autoVerboseTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get(health.VerboseTokenHeader) == "" {
		req = req.Clone(req.Context())
		req.Header.Set(health.VerboseTokenHeader, testVerboseToken)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// testHTTPClient is used in place of http.DefaultClient to prevent test
// hangs on stalled connections (e.g., during shutdown races). The
// autoVerboseTokenTransport transparently attaches the PR-A35 verbose
// token header so existing /readyz?verbose calls keep working.
var testHTTPClient = &http.Client{
	Timeout:   2 * time.Second,
	Transport: &autoVerboseTokenTransport{},
}

// newTestBootstrap is the canonical constructor for tests in this file.
// It wires WithVerboseToken(testVerboseToken) so that /readyz?verbose
// requests accompanied by testHTTPClient (which auto-attaches the header)
// are served the verbose body. Tests covering the token gate itself must
// still call New() directly to exercise the missing/mismatched paths.
func newTestBootstrap(opts ...Option) *Bootstrap {
	return New(append([]Option{WithVerboseToken(testVerboseToken)}, opts...)...)
}

// decodeSuccessBody reads a `{"data": {...}}` envelope from an http.Response
// and returns the inner map. PR-A35 aligned /readyz to the same envelope
// used by business endpoints so every 200 response in this test file goes
// through one helper instead of hand-rolling `body["data"].(map)` at every
// call site.
func decodeSuccessBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var envelope map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	data, ok := envelope["data"].(map[string]any)
	require.True(t, ok, "response must carry data envelope; got %v", envelope)
	return data
}

// decodeErrorDetails reads an `{"error": {"code":..., "details":{...}}}`
// envelope and returns the details map alongside the code. Used by tests
// that assert a 503 /readyz response surfaces the expected probe-level
// breakdown inside details.
func decodeErrorDetails(t *testing.T, resp *http.Response) (code string, details map[string]any) {
	t.Helper()
	var envelope map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	errObj, ok := envelope["error"].(map[string]any)
	require.True(t, ok, "response must carry error envelope; got %v", envelope)
	codeStr, _ := errObj["code"].(string)
	det, _ := errObj["details"].(map[string]any)
	if det == nil {
		det = map[string]any{}
	}
	return codeStr, det
}

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
	assert.Equal(t, ":8080", b.primaryAddr)
	assert.Nil(t, b.assembly)
	assert.Nil(t, b.publisher)
	assert.Nil(t, b.subscriber)
}

func TestNew_WithOptions(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	eb := eventbus.New()

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPublisher(eb), WithSubscriber(eb),
		WithHTTPPrimaryAddr(":9090"),
		WithShutdownTimeout(5*time.Second),
	)

	assert.Equal(t, ":9090", b.primaryAddr)
	assert.Equal(t, asm, b.assembly)
	assert.Equal(t, eb, b.publisher)
	assert.Equal(t, eb, b.subscriber)
	assert.Equal(t, 5*time.Second, b.shutdownTimeout)
}

// TestNew_VerboseConfig is the single source of truth for how /readyz
// verbose options propagate from constructor into the Bootstrap struct.
// Runtime behaviour (200/401/503 + envelope shape) is exercised end-to-end
// by runtime/http/health.TestReadyz_VerboseToken_StrictDeny — there is no
// reason to repeat that table here.
func TestNew_VerboseConfig(t *testing.T) {
	tests := []struct {
		name         string
		opts         []Option
		wantToken    string
		wantDisabled bool
	}{
		{
			name:         "default",
			opts:         nil,
			wantToken:    "",
			wantDisabled: false,
		},
		{
			name:      "WithVerboseToken populates verboseToken",
			opts:      []Option{WithVerboseToken("secret-123")},
			wantToken: "secret-123",
		},
		{
			name:         "WithVerboseDisabled flips verboseDisabled",
			opts:         []Option{WithVerboseDisabled()},
			wantDisabled: true,
		},
		{
			name:         "both options coexist (DISABLED wins at request time)",
			opts:         []Option{WithVerboseToken("secret-123"), WithVerboseDisabled()},
			wantToken:    "secret-123",
			wantDisabled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(tt.opts...)
			assert.Equal(t, tt.wantToken, b.verboseToken)
			assert.Equal(t, tt.wantDisabled, b.verboseDisabled)
		})
	}
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
	asm := assembly.New(assembly.Config{ID: "test-proxy-err", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
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
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
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
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("a")))
	require.NoError(t, asm.Register(newTestCell("b")))

	ids := asm.CellIDs()
	assert.Equal(t, []string{"a", "b"}, ids)
}

func TestBootstrap_CellLookup(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
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
	r.AddContractHandler(testEventSpec("test.context"), func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
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

func (s *invokeOnceSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *invokeOnceSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *invokeOnceSubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, handler outbox.EntryHandler) error {
	s.once.Do(func() {
		handler(ctx, s.entry)
	})
	<-ctx.Done()
	return ctx.Err()
}

func (s *invokeOnceSubscriber) Close(_ context.Context) error { return nil }

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
	r.AddContractHandler(testEventSpec("test.topic"), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
	return nil
}

func TestBootstrap_MissingSubscriber_WithEventRegistrar_Fails(t *testing.T) {
	// When a cell implements EventRegistrar but no subscriber is configured,
	// bootstrap must fail at startup instead of silently skipping all subscriptions.
	asm := assembly.New(assembly.Config{ID: "test-no-sub", DurabilityMode: cell.DurabilityDemo})
	ec := newEventCell("needs-sub", nil) // registers a handler
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := newTestBootstrap(
		WithAssembly(asm),
		WithPublisher(eb),
		// WithSubscriber intentionally omitted.
		WithHTTPPrimaryAddr("127.0.0.1:0"),
	)

	err := b.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EventRegistrar")
	assert.Contains(t, err.Error(), "no subscriber")
}

func TestBootstrap_SubscriptionFailure_TriggersRollback(t *testing.T) {
	// S3-03: When RegisterSubscriptions fails, Run must rollback previously
	// started components (assembly) and return an error wrapping the cause.
	asm := assembly.New(assembly.Config{ID: "test-rollback", DurabilityMode: cell.DurabilityDemo})
	ec := newEventCell("fail-cell", errors.New("DLX not configured"))
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := newTestBootstrap(
		WithAssembly(asm),
		WithPublisher(eb), WithSubscriber(eb),
		WithHTTPPrimaryAddr("127.0.0.1:0"),
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

	asm := assembly.New(assembly.Config{ID: "test-router-ok", DurabilityMode: cell.DurabilityDemo})
	ec := newEventCell("ok-cell", nil) // nil error → registers 1 handler
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := newTestBootstrap(
		WithAssembly(asm),
		WithSubscriber(eb),
		WithPublisher(eb),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-router-context", DurabilityMode: cell.DurabilityDemo})
	got := make(chan map[string]string, 1)
	require.NoError(t, asm.Register(newContextCaptureCell("capture-cell", got)))

	sub := &invokeOnceSubscriber{entry: outbox.Entry{
		ID:        "evt-context-1",
		EventType: "test.context",
		Observability: outbox.ObservabilityMetadata{
			RequestID:     "req-ctx-1",
			CorrelationID: "corr-ctx-1",
			TraceID:       "trace-ctx-1",
		},
	}}

	b := newTestBootstrap(
		WithAssembly(asm),
		WithSubscriber(sub),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

func TestBootstrap_RunContextCancel(t *testing.T) {
	// Test that Run returns when context is cancelled immediately,
	// even though it will fail at listen (sandbox restriction).
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	b := New(
		WithHTTPPrimaryAddr("127.0.0.1:0"),
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

	b := New(WithHTTPPrimaryAddr("127.0.0.1:0"))
	_ = b.Run(ctx) // first call — may error due to cancelled ctx or sandbox

	err := b.Run(ctx) // second call — must be rejected
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Run called more than once")
}

func TestBootstrap_WithHealthChecker_Healthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-hc-healthy", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func(_ context.Context) error { return nil }),
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

	body := decodeSuccessBody(t, resp)
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	rabbitmq, ok := deps["rabbitmq"].(map[string]any)
	require.True(t, ok, "rabbitmq entry must be a map")
	assert.Equal(t, "healthy", rabbitmq["status"])

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

	asm := assembly.New(assembly.Config{ID: "test-hc-unhealthy", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func(_ context.Context) error {
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

	code, details := decodeErrorDetails(t, resp)
	assert.Equal(t, "ERR_READYZ_UNHEALTHY", code)
	deps, ok := details["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map in error details")
	rabbitmq, ok := deps["rabbitmq"].(map[string]any)
	require.True(t, ok, "rabbitmq entry must be a map")
	assert.Equal(t, "unhealthy", rabbitmq["status"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_WithAdapterInfo_AppearsInReadyz(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-adapter-info", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAdapterInfo(map[string]string{
			"mode":    "in-memory",
			"storage": "in-memory",
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

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	body := decodeSuccessBody(t, resp)
	adapters, ok := body["adapters"].(map[string]any)
	require.True(t, ok, "verbose readyz must contain adapters map")
	assert.Equal(t, "in-memory", adapters["mode"])
	assert.Equal(t, "in-memory", adapters["storage"])

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// --- HealthContributor discovery tests ---

// healthContribCell is a Cell that implements cell.HealthContributor.
type healthContribCell struct {
	*cell.BaseCell
	checkers map[string]func(context.Context) error
}

func newHealthContribCell(id string, checkers map[string]func(context.Context) error) *healthContribCell {
	return &healthContribCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		checkers: checkers,
	}
}

func (c *healthContribCell) HealthCheckers() map[string]func(context.Context) error {
	return c.checkers
}

var _ cell.HealthContributor = (*healthContribCell)(nil)

func TestBootstrap_HealthContributor_Discovery_AppearsInReadyz(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-hc-contrib", DurabilityMode: cell.DurabilityDemo})
	hcc := newHealthContribCell("accesscore", map[string]func(context.Context) error{
		"session-store": func(_ context.Context) error { return nil },
	})
	require.NoError(t, asm.Register(hcc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeSuccessBody(t, resp)
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	sessionStore, ok := deps["session-store"].(map[string]any)
	require.True(t, ok, "session-store entry must be a map")
	assert.Equal(t, "healthy", sessionStore["status"],
		"HealthContributor-discovered probe should appear in /readyz verbose")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_HealthContributor_DuplicateName_FailsFast(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-hc-dup", DurabilityMode: cell.DurabilityDemo})
	// Two cells both return "session-store" probe — should conflict.
	require.NoError(t, asm.Register(newHealthContribCell("cell-a", map[string]func(context.Context) error{
		"session-store": func(_ context.Context) error { return nil },
	})))
	require.NoError(t, asm.Register(newHealthContribCell("cell-b", map[string]func(context.Context) error{
		"session-store": func(_ context.Context) error { return nil },
	})))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = b.Run(ctx)
	require.Error(t, err, "duplicate probe names across cells should fail")
	assert.Contains(t, err.Error(), "duplicate health checker")
	assert.Contains(t, err.Error(), "session-store")
}

func TestWithHealthChecker_EmptyName_ReturnsError(t *testing.T) {
	b := New(
		WithHealthChecker("", func(_ context.Context) error { return nil }),
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
		WithHealthChecker("", func(_ context.Context) error { return nil }),
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

// mockAllowerForBootstrap is a minimal Allower used only in bootstrap option tests.
type mockAllowerForBootstrap struct{}

func (m *mockAllowerForBootstrap) Allow() (bool, func(error)) {
	return true, func(error) {}
}

func TestWithCircuitBreaker_NilInterface_Error(t *testing.T) {
	// A bare nil interface must cause Run() to fail-fast with a descriptive
	// error rather than silently leaving the service without CB protection.
	b := New(WithCircuitBreaker(nil))
	err := b.Run(context.Background())
	require.Error(t, err, "nil interface Allower must return error from Run")
	assert.Contains(t, err.Error(), "circuit breaker")
}

func TestWithCircuitBreaker_TypedNilPointer_Error(t *testing.T) {
	// A typed-nil (*mockAllowerForBootstrap)(nil) has a non-nil interface value
	// but a nil underlying pointer. Calling Allow() on it would panic at runtime.
	// Bootstrap must detect and reject it just like a bare nil.
	var cb *mockAllowerForBootstrap // typed nil
	b := New(WithCircuitBreaker(cb))
	err := b.Run(context.Background())
	require.Error(t, err, "typed-nil Allower must return error from Run")
	assert.Contains(t, err.Error(), "circuit breaker")
}

func TestBootstrap_WithMultipleHealthCheckers_OneUnhealthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-multi-hc", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func(_ context.Context) error { return nil }),
		WithHealthChecker("postgres", func(_ context.Context) error { return fmt.Errorf("connection refused") }),
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

	code, details := decodeErrorDetails(t, resp)
	assert.Equal(t, "ERR_READYZ_UNHEALTHY", code,
		"overall status must surface the unhealthy errcode")
	deps, ok := details["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map in error details")
	rabbitmqEntry, ok := deps["rabbitmq"].(map[string]any)
	require.True(t, ok, "rabbitmq entry must be a map")
	assert.Equal(t, "healthy", rabbitmqEntry["status"], "rabbitmq checker should be healthy")
	postgresEntry, ok := deps["postgres"].(map[string]any)
	require.True(t, ok, "postgres entry must be a map")
	assert.Equal(t, "unhealthy", postgresEntry["status"], "postgres checker should be unhealthy")

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

	asm := assembly.New(assembly.Config{ID: "test-dynamic-hc", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	// Atomic flag to simulate connection health transitions at runtime.
	var unhealthy atomic.Bool

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithHealthChecker("rabbitmq", func(_ context.Context) error {
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

	asm := assembly.New(assembly.Config{ID: "test-config-watcher-readyz", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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
		var envelope map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return false
		}
		data, ok := envelope["data"].(map[string]any)
		if !ok {
			return false
		}
		deps, ok := data["dependencies"].(map[string]any)
		if !ok {
			return false
		}
		probe, ok := deps[configWatcherCheckerName].(map[string]any)
		if !ok {
			return false
		}
		return probe["status"] == "healthy"
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

	asm := assembly.New(assembly.Config{ID: "test-config-drift-no-drift", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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
		var envelope map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return false
		}
		data, ok := envelope["data"].(map[string]any)
		if !ok {
			return false
		}
		deps, ok := data["dependencies"].(map[string]any)
		if !ok {
			return false
		}
		// Config drift checker should be registered and healthy (no drift).
		probe, ok := deps[configDriftCheckerName].(map[string]any)
		if !ok {
			return false
		}
		return probe["status"] == "healthy"
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

func TestBootstrap_ConfigDriftReadyz_HTTP503OnDrift(t *testing.T) {
	// Integration test: verify /readyz returns 503 when config drift exists.
	// Uses a reloaderCell that always fails → observedGeneration never advances.
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	failCell := newReloaderCell("fail-cell")
	failCell.err = fmt.Errorf("intentional reload failure")

	asm := assembly.New(assembly.Config{ID: "test-drift-http-503", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(failCell))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	// Wait for server to be ready (healthy initially — no drift yet).
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "server did not become ready")

	// Trigger a config change → watcher fires OnChange → Reload → generation++
	// → failCell.OnConfigReload returns error → observedGeneration stays → drift!
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: drifted\n"), 0o644))

	// Poll /readyz?verbose until config-drift shows unhealthy.
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			return false
		}
		var envelope map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return false
		}
		errObj, ok := envelope["error"].(map[string]any)
		if !ok {
			return false
		}
		details, ok := errObj["details"].(map[string]any)
		if !ok {
			return false
		}
		deps, ok := details["dependencies"].(map[string]any)
		if !ok {
			return false
		}
		probe, ok := deps[configDriftCheckerName].(map[string]any)
		if !ok {
			return false
		}
		return probe["status"] == "unhealthy"
	}, 5*time.Second, 100*time.Millisecond, "readyz should return 503 with config-drift unhealthy")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_ConfigWatcherInitFailure_FailsFast(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("app:\n  name: test\n"), 0o644))

	asm := assembly.New(assembly.Config{ID: "test-config-watcher-fail-fast", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
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

	asm := assembly.New(assembly.Config{ID: "test-reserved-health-checker", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithHealthChecker("config-watcher", func(_ context.Context) error { return nil }),
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

	asm := assembly.New(assembly.Config{ID: "test-eventrouter-readyz", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newEventCell("ok-cell", nil)))

	eb := eventbus.New()
	b := newTestBootstrap(
		WithAssembly(asm),
		WithPublisher(eb),
		WithSubscriber(eb),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	body := decodeSuccessBody(t, resp)
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain dependencies")
	erProbe, ok := deps["eventrouter"].(map[string]any)
	require.True(t, ok, "eventrouter probe must be a structured ProbeResult")
	assert.Equal(t, "healthy", erProbe["status"])

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

	asm := assembly.New(assembly.Config{ID: "test-drain", DurabilityMode: cell.DurabilityDemo})
	slow := newSlowReloaderCell("slow-cell", 300*time.Millisecond)
	require.NoError(t, asm.Register(slow))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-reload", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("auth-core")
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-reload-err", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("fail-cell")
	rc.err = errors.New("reload callback failed")
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-reload-panic", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("panic-cell")
	rc.doPanic = true
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-reload-fifo", DurabilityMode: cell.DurabilityDemo})
	callOrder := make([]string, 0, 3)
	cells := make([]*reloaderCell, 3)
	for i, id := range []string{"first", "second", "third"} {
		cells[i] = newReloaderCell(id)
		cells[i].callOrder = &callOrder
		require.NoError(t, asm.Register(cells[i]))
	}

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-reload-skip", DurabilityMode: cell.DurabilityDemo})
	plain := newTestCell("plain-cell") // does NOT implement ConfigReloader
	rc := newReloaderCell("reloader-cell")
	require.NoError(t, asm.Register(plain))
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-reload-noop", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("noop-cell")
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-isolation", DurabilityMode: cell.DurabilityDemo})
	mutator := newMutatingReloaderCell("mutator")
	observer := newReloaderCell("observer")
	// Register mutator first — it tries to corrupt the event.
	require.NoError(t, asm.Register(mutator))
	require.NoError(t, asm.Register(observer))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-shutdown-race", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("shutdown-race-cell")
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-shutdown-drain-reject", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("shutdown-drain-cell")
	require.NoError(t, asm.Register(rc))

	blocker := newBlockingStopWorker()
	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	asm := assembly.New(assembly.Config{ID: "test-generation", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("gen-cell")
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/data"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}), Policy: auth.Authenticated()})
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodPost, "/api/v1/access/sessions/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"token":"test"}}`))
	}), Public: true})
}

// bootstrapTestVerifier is a minimal IntentTokenVerifier for bootstrap tests.
type bootstrapTestVerifier struct {
	claims    auth.Claims
	err       error
	callCount atomic.Int32
}

func (v *bootstrapTestVerifier) Verify(_ context.Context, _ string) (auth.Claims, error) {
	v.callCount.Add(1)
	return v.claims, v.err
}

func (v *bootstrapTestVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	v.callCount.Add(1)
	return v.claims, v.err
}

func TestBootstrap_WithAuthMiddleware_ProtectedRoute_Returns401(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-401", DurabilityMode: cell.DurabilityDemo})
	hc := newHTTPCell("auth-test-cell")
	require.NoError(t, asm.Register(hc))

	verifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthMiddleware(verifier),
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

// publicHTTPCell registers the login route as public via auth.Mount(Public:true),
// following the F3 pattern where cells own their auth declarations.
type publicHTTPCell struct {
	*cell.BaseCell
}

func newPublicHTTPCell(id string) *publicHTTPCell {
	return &publicHTTPCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
	}
}

func (c *publicHTTPCell) RegisterRoutes(mux cell.RouteMux) {
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/data"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}), Public: true})
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodPost, "/api/v1/access/sessions/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"token":"test"}}`))
	}), Public: true})
}

func TestBootstrap_WithAuthMiddleware_PublicRoute_Passes(t *testing.T) {
	// F3: public routes are declared via auth.Mount(Public:true) inside the cell.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-public", DurabilityMode: cell.DurabilityDemo})
	hc := newPublicHTTPCell("auth-public-cell")
	require.NoError(t, asm.Register(hc))

	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("should not verify for public route"),
	}

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthMiddleware(verifier),
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

	asm := assembly.New(assembly.Config{ID: "test-health-override", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	// Create a custom health handler backed by an un-started assembly (unhealthy).
	// If the user's handler wins, /readyz would return 503 because the custom
	// assembly was never started.
	customAsm := assembly.New(assembly.Config{ID: "custom-unstartled", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, customAsm.Register(newTestCell("custom-cell")))
	customHandler := health.New(customAsm) // un-started → always unhealthy

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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
	b := New(
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
	)
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
	// PR-A14a: register via raw mux.Handle + coverage whitelist so the route
	// is neither Public (which starts a new trace root) nor policy-guarded
	// (which would 401 without a verifier). Handler runs, tracing works.
	tc := newTracingTestCell("trace-biz", func(mux cell.RouteMux) {
		mux.Handle("GET /api/v1/trace-test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
	})

	asm := assembly.New(assembly.Config{ID: "trace-e2e", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(tc))

	ctx, cancel := context.WithCancel(context.Background())
	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithTracer(tracer),
		WithRouterOptions(router.WithPolicyCoverageWhitelist([]string{"/api/v1/*"})),
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
	// PR-A14a: see TestBootstrap_TracingE2E_BusinessRoute rationale for the
	// raw-Handle + whitelist pattern.
	tc := newTracingTestCell("trace-upstream", func(mux cell.RouteMux) {
		mux.Handle("GET /api/v1/propagate", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
	})

	asm := assembly.New(assembly.Config{ID: "trace-upstream", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(tc))

	ctx, cancel := context.WithCancel(context.Background())
	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithTracer(tracer),
		WithRouterOptions(router.WithPolicyCoverageWhitelist([]string{"/api/v1/*"})),
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

	asm := assembly.New(assembly.Config{ID: "trace-panic", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(tc))

	ctx, cancel := context.WithCancel(context.Background())
	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithTracer(tracer),
		WithRouterOptions(router.WithPolicyCoverageWhitelist([]string{"/api/v1/*"})),
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
	// Round-4 F4: middleware.Tracing applies DefaultProbeFilter by default,
	// so /healthz / /readyz / /livez / /metrics no longer produce a span
	// (and therefore no trace_id in their access logs). This reverses the
	// pre-round-4 behaviour where probe routes were traced by accident.
	// High-frequency infra traffic no longer consumes span / metric budget.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(oldDefault)

	ln := newLocalListener(t)
	tracer := tracing.NewTracer("bootstrap-infra-e2e")

	ctx, cancel := context.WithCancel(context.Background())
	b := New(
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
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

	// Probe endpoint must NOT emit trace_id — F4 DefaultProbeFilter pre-empts span creation.
	logOutput := buf.String()
	assert.NotContains(t, logOutput, "trace_id",
		"round-4 F4: /healthz span creation is skipped by DefaultProbeFilter so no trace_id is emitted")
}

// --- Auth Provider discovery (post-Init cell discovery) ---

// authProviderCell implements HTTPRegistrar and exposes an IntentTokenVerifier.
type authProviderCell struct {
	*cell.BaseCell
	verifier auth.IntentTokenVerifier
}

func newAuthProviderCell(id string, verifier auth.IntentTokenVerifier) *authProviderCell {
	return &authProviderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		verifier: verifier,
	}
}

func (c *authProviderCell) TokenVerifier() auth.IntentTokenVerifier {
	return c.verifier
}

func (c *authProviderCell) RegisterRoutes(mux cell.RouteMux) {
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/data"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}), Policy: auth.Authenticated()})
	// F3: login is declared as a public route so auth discovery tests can verify
	// that no-token requests bypass JWT checks on this endpoint.
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodPost, "/api/v1/access/sessions/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"login-ok"}`))
	}), Public: true})
}

func TestBootstrap_AuthDiscovery_ProtectedRoute_Returns401(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-auth-discovery-401", DurabilityMode: cell.DurabilityDemo})
	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("no token provided"),
	}
	hc := newAuthProviderCell("accesscore", verifier)
	require.NoError(t, asm.Register(hc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthDiscovery(),
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

	asm := assembly.New(assembly.Config{ID: "test-auth-discovery-public", DurabilityMode: cell.DurabilityDemo})
	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("should not verify for public route"),
	}
	hc := newAuthProviderCell("accesscore", verifier)
	require.NoError(t, asm.Register(hc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		// F3: public routes are declared via auth.Mount(Public:true) in the cell.
		WithAuthDiscovery(),
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

	// Method-specific bypass regression: GET must return 401 (only POST is public).
	getReq, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://%s/api/v1/access/sessions/login", addr), nil)
	require.NoError(t, err)
	getResp, err := testHTTPClient.Do(getReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, getResp.StatusCode,
		"GET must not bypass auth when only POST is declared public (method-specific bypass)")

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

	asm := assembly.New(assembly.Config{ID: "test-auth-precedence", DurabilityMode: cell.DurabilityDemo})

	cellVerifier := &bootstrapTestVerifier{
		err: fmt.Errorf("cell-verifier: should not be called"),
	}
	hc := newAuthProviderCell("accesscore", cellVerifier)
	require.NoError(t, asm.Register(hc))

	explicitVerifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "explicit-user", Roles: []string{"admin"}},
	}

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthMiddleware(explicitVerifier),
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
	asm := assembly.New(assembly.Config{ID: "test-no-auth-provider", DurabilityMode: cell.DurabilityDemo})
	hc := newHTTPCell("plain-cell")
	require.NoError(t, asm.Register(hc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthDiscovery(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run should fail because no auth provider cell was discovered.
	err = b.Run(ctx)
	require.Error(t, err, "bootstrap should fail when no auth provider cell is discovered")
	assert.Contains(t, err.Error(), "auth provider cell",
		"error should mention missing auth provider")
}

func TestBootstrap_AuthDiscovery_MultipleProviders_FailsFast(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	verifier1 := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	verifier2 := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-2", Roles: []string{"admin"}},
	}

	asm := assembly.New(assembly.Config{ID: "test-multi-auth", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newAuthProviderCell("accesscore", verifier1)))
	require.NoError(t, asm.Register(newAuthProviderCell("identity-core", verifier2)))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthDiscovery(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = b.Run(ctx)
	require.Error(t, err, "bootstrap should reject multiple auth provider cells")
	assert.Contains(t, err.Error(), "multiple auth provider cells")
	assert.Contains(t, err.Error(), "accesscore")
	assert.Contains(t, err.Error(), "identity-core")
}

// TestBootstrap_TrustBoundary_PublicEndpoint_IgnoresClientIDs verifies that
// bootstrap auto-wiring correctly passes authPublicEndpoints to the request_id
// middleware. Public endpoints must reject client-supplied X-Request-Id headers
// (preventing untrusted callers from injecting observability identifiers),
// while protected endpoints must accept trusted upstream IDs.
func TestBootstrap_TrustBoundary_PublicEndpoint_IgnoresClientIDs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-trust-boundary", DurabilityMode: cell.DurabilityDemo})
	verifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	hc := newAuthProviderCell("accesscore", verifier)
	require.NoError(t, asm.Register(hc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		// F3: public routes declared via auth.Mount(Public:true) in authProviderCell.
		WithAuthDiscovery(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// --- Public endpoint: client-supplied X-Request-Id must be ignored ---
	t.Run("public endpoint ignores client-supplied request ID", func(t *testing.T) {
		req, reqErr := http.NewRequest("POST",
			fmt.Sprintf("http://%s/api/v1/access/sessions/login", addr), nil)
		require.NoError(t, reqErr)
		req.Header.Set("X-Request-Id", "attacker-injected-id")

		resp, respErr := testHTTPClient.Do(req)
		require.NoError(t, respErr)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		actualID := resp.Header.Get("X-Request-Id")
		assert.NotEmpty(t, actualID, "response must have X-Request-Id")
		assert.NotEqual(t, "attacker-injected-id", actualID,
			"public endpoint must generate a fresh request ID, not accept client-supplied value")
	})

	// --- Protected endpoint: trusted upstream X-Request-Id must be accepted ---
	t.Run("protected endpoint accepts trusted upstream request ID", func(t *testing.T) {
		req, reqErr := http.NewRequest("GET",
			fmt.Sprintf("http://%s/api/v1/data", addr), nil)
		require.NoError(t, reqErr)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-Request-Id", "trusted-upstream-id")

		resp, respErr := testHTTPClient.Do(req)
		require.NoError(t, respErr)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "trusted-upstream-id", resp.Header.Get("X-Request-Id"),
			"protected endpoint must accept trusted upstream X-Request-Id")
	})

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

func TestBootstrap_WithSecurityHeadersOptions_CustomHSTS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-sechdr", DurabilityMode: cell.DurabilityDemo})
	tc := newTestCell("hsts-cell")
	require.NoError(t, asm.Register(tc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithSecurityHeadersOptions(
			middleware.WithHSTSIncludeSubDomains(),
			middleware.WithHSTSPreload(),
		),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, reqErr := testHTTPClient.Get("http://" + addr + "/healthz")
	require.NoError(t, reqErr)
	_ = resp.Body.Close()

	hsts := resp.Header.Get("Strict-Transport-Security")
	assert.Contains(t, hsts, "includeSubDomains",
		"WithSecurityHeadersOptions must propagate HSTS subdomain directive")
	assert.Contains(t, hsts, "preload",
		"WithSecurityHeadersOptions must propagate HSTS preload directive")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// ConfigKeyFilterer integration tests (CFG-KEYFILTER-WIRE-01)
// ---------------------------------------------------------------------------

// keyFilterReloaderCell implements ConfigReloader + ConfigKeyFilterer for testing.
type keyFilterReloaderCell struct {
	*cell.BaseCell
	prefixes []string
	reloaded chan cell.ConfigChangeEvent
}

func newKeyFilterReloaderCell(id string, prefixes []string) *keyFilterReloaderCell {
	return &keyFilterReloaderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
		prefixes: prefixes,
		reloaded: make(chan cell.ConfigChangeEvent, 4),
	}
}

func (c *keyFilterReloaderCell) OnConfigReload(event cell.ConfigChangeEvent) error {
	c.reloaded <- event
	return nil
}

func (c *keyFilterReloaderCell) ConfigKeyPrefixes() []string {
	return c.prefixes
}

// TestBootstrap_ConfigReload_KeyFilter_SkipsUnmatched verifies that a cell with
// ConfigKeyPrefixes()=["server."] is NOT notified when only "db.host" changes.
func TestBootstrap_ConfigReload_KeyFilter_SkipsUnmatched(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("db:\n  host: localhost\n"), 0o644))

	ln := newLocalListener(t)

	asm := assembly.New(assembly.Config{ID: "test-keyfilter-skip", DurabilityMode: cell.DurabilityDemo})
	kfc := newKeyFilterReloaderCell("server-cell", []string{"server."})
	require.NoError(t, asm.Register(kfc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Change only a db.* key — server. cell must NOT be notified.
	require.NoError(t, os.WriteFile(cfgFile, []byte("db:\n  host: db-primary\n"), 0o644))

	// Wait long enough for any spurious notification to arrive.
	select {
	case evt := <-kfc.reloaded:
		t.Fatalf("cell must NOT be notified when keys don't match prefix: got event %+v", evt)
	case <-time.After(500 * time.Millisecond):
		// expected: no notification
	}

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestBootstrap_ConfigReload_KeyFilter_NotifiesMatched verifies that a cell with
// ConfigKeyPrefixes()=["server."] IS notified when "server.port" changes.
func TestBootstrap_ConfigReload_KeyFilter_NotifiesMatched(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("server:\n  port: 8080\n"), 0o644))

	ln := newLocalListener(t)

	asm := assembly.New(assembly.Config{ID: "test-keyfilter-match", DurabilityMode: cell.DurabilityDemo})
	kfc := newKeyFilterReloaderCell("server-cell", []string{"server."})
	require.NoError(t, asm.Register(kfc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Change a server.* key — server. cell MUST be notified.
	require.NoError(t, os.WriteFile(cfgFile, []byte("server:\n  port: 9090\ndb:\n  host: localhost\n"), 0o644))

	select {
	case evt := <-kfc.reloaded:
		assert.Contains(t, evt.Updated, "server.port",
			"event must contain the matched key")
		// Minimal exposure: Config snapshot only contains keys matching the
		// cell's registered prefixes, not the full config.
		_, hasServer := evt.Config["server.port"]
		_, hasDB := evt.Config["db.host"]
		assert.True(t, hasServer, "Config must contain matched prefix keys")
		assert.False(t, hasDB, "Config must NOT contain keys outside registered prefixes (minimal exposure)")
	case <-time.After(3 * time.Second):
		t.Fatal("cell was not notified after matching key change")
	}

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestBootstrap_ConfigReload_NoKeyFilter_ReceivesAll verifies backward
// compatibility: a cell implementing only ConfigReloader (no ConfigKeyFilterer)
// receives all config change notifications regardless of which keys changed.
func TestBootstrap_ConfigReload_NoKeyFilter_ReceivesAll(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("db:\n  host: localhost\n"), 0o644))

	ln := newLocalListener(t)

	asm := assembly.New(assembly.Config{ID: "test-keyfilter-nofilter", DurabilityMode: cell.DurabilityDemo})
	rc := newReloaderCell("plain-reloader")
	require.NoError(t, asm.Register(rc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithConfig(cfgFile, ""),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// Change any key — plain reloader must receive notification.
	require.NoError(t, os.WriteFile(cfgFile, []byte("db:\n  host: db-primary\n"), 0o644))

	require.Eventually(t, func() bool {
		return rc.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond, "plain ConfigReloader must receive all notifications")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// Trust boundary: traceparent header injection (F2-SEC-03)
// ---------------------------------------------------------------------------

// traceCapturingCell registers routes that capture the trace_id from context.
type traceCapturingCell struct {
	*cell.BaseCell
	verifier     auth.IntentTokenVerifier
	gotPublic    chan string
	gotProtected chan string
}

func newTraceCapturingCell(id string, verifier auth.IntentTokenVerifier) *traceCapturingCell {
	return &traceCapturingCell{
		BaseCell:     cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		verifier:     verifier,
		gotPublic:    make(chan string, 4),
		gotProtected: make(chan string, 4),
	}
}

func (c *traceCapturingCell) TokenVerifier() auth.IntentTokenVerifier {
	return c.verifier
}

func (c *traceCapturingCell) RegisterRoutes(mux cell.RouteMux) {
	// F3: public/ping is declared public so it creates new trace roots and
	// rejects client-supplied request IDs.
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/public/ping"), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, _ := ctxkeys.TraceIDFrom(r.Context())
		c.gotPublic <- tid
		w.WriteHeader(http.StatusOK)
	}), Public: true})
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/protected/ping"), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, _ := ctxkeys.TraceIDFrom(r.Context())
		c.gotProtected <- tid
		w.WriteHeader(http.StatusOK)
	}), Policy: auth.Authenticated()})
}

// TestBootstrap_TrustBoundary_PublicEndpoint_TraceparentIgnored verifies the
// trust boundary for traceparent headers:
//   - Public endpoints must NOT propagate a client-supplied traceparent (new root trace).
//   - Protected endpoints with a valid auth token MUST propagate the upstream traceparent.
func TestBootstrap_TrustBoundary_PublicEndpoint_TraceparentIgnored(t *testing.T) {
	ln := newLocalListener(t)
	tracer := tracing.NewTracer("trust-test")

	verifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	tc := newTraceCapturingCell("trace-boundary-cell", verifier)

	asm := assembly.New(assembly.Config{ID: "test-traceparent-boundary", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(tc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithTracer(tracer),
		WithShutdownTimeout(2*time.Second),
		// F3: /api/v1/public/ping is declared public by traceCapturingCell.
		WithAuthDiscovery(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	upstreamTraceID := "aabbccddeeff00112233445566778899"
	traceparentHeader := "00-" + upstreamTraceID + "-b7ad6b7169203331-01"

	t.Run("public endpoint creates new root trace", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("http://%s/api/v1/public/ping", addr), nil)
		require.NoError(t, err)
		req.Header.Set("traceparent", traceparentHeader)

		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var gotTraceID string
		select {
		case gotTraceID = <-tc.gotPublic:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not capture trace_id")
		}
		assert.NotEmpty(t, gotTraceID, "trace_id must be set in handler context")
		assert.NotEqual(t, upstreamTraceID, gotTraceID,
			"public endpoint must create a new root trace, not propagate client-supplied traceparent")
	})

	t.Run("protected endpoint continues upstream trace", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("http://%s/api/v1/protected/ping", addr), nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("traceparent", traceparentHeader)

		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var gotTraceID string
		select {
		case gotTraceID = <-tc.gotProtected:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not capture trace_id")
		}
		assert.Equal(t, upstreamTraceID, gotTraceID,
			"protected endpoint must propagate upstream trace_id from traceparent header")
	})

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// Auth option conflict detection (P1 fail-fast)
// ---------------------------------------------------------------------------

func TestBootstrap_ConflictingAuthOptions_ReturnsError(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "conflict-test", DurabilityMode: cell.DurabilityDemo})
	tc := newTestCell("conflict-cell")
	require.NoError(t, asm.Register(tc))

	verifier := &bootstrapTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}

	t.Run("WithAuthMiddleware then WithAuthDiscovery", func(t *testing.T) {
		b := New(
			WithAssembly(asm),
			WithAuthMiddleware(verifier),
			WithAuthDiscovery(),
		)
		err := b.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("WithAuthDiscovery then WithAuthMiddleware", func(t *testing.T) {
		b := New(
			WithAssembly(asm),
			WithAuthDiscovery(),
			WithAuthMiddleware(verifier),
		)
		err := b.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})
}

// ---------------------------------------------------------------------------
// Circuit breaker nil option detection (P1-A fail-fast)
// ---------------------------------------------------------------------------

func TestBootstrap_WithCircuitBreaker_Nil_ReturnsError(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "cb-nil-test", DurabilityMode: cell.DurabilityDemo})
	tc := newTestCell("cb-nil-cell")
	require.NoError(t, asm.Register(tc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithCircuitBreaker(nil),
	)
	err := b.Run(context.Background())
	require.Error(t, err, "nil Allower must cause Run to return an error")
	assert.Contains(t, err.Error(), "circuit breaker")
}

// publicPingAuthCell is a test cell that provides TokenVerifier (for discovery)
// and registers GET /api/v1/public/ping as a public route.
type publicPingAuthCell struct {
	*cell.BaseCell
	verifier auth.IntentTokenVerifier
}

func newPublicPingAuthCell(id string, verifier auth.IntentTokenVerifier) *publicPingAuthCell {
	return &publicPingAuthCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		verifier: verifier,
	}
}

func (c *publicPingAuthCell) TokenVerifier() auth.IntentTokenVerifier { return c.verifier }

func (c *publicPingAuthCell) RegisterRoutes(mux cell.RouteMux) {
	// F3: GET /api/v1/public/ping is declared public; HEAD alias is automatic.
	auth.Mount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/public/ping"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Public: true})
}

// TestBootstrap_HEADAlias_BypassesAuth tests I-17: GET public endpoint
// declaration automatically covers HEAD requests (RFC 7231 §4.3.2).
func TestBootstrap_HEADAlias_BypassesAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-head-alias", DurabilityMode: cell.DurabilityDemo})
	verifier := &bootstrapTestVerifier{
		err: fmt.Errorf("should not be called for GET/HEAD public route"),
	}
	// F3: cell declares GET /api/v1/public/ping as public; HEAD alias is automatic.
	hc := newPublicPingAuthCell("accesscore", verifier)
	require.NoError(t, asm.Register(hc))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithAuthDiscovery(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	// HEAD request to the GET-declared public endpoint must bypass auth.
	headReq, err := http.NewRequest(http.MethodHead,
		fmt.Sprintf("http://%s/api/v1/public/ping", addr), nil)
	require.NoError(t, err)
	headResp, err := testHTTPClient.Do(headReq)
	require.NoError(t, err)
	defer headResp.Body.Close()

	// HEAD to a public GET endpoint should not return 401 (verifier not called).
	assert.NotEqual(t, http.StatusUnauthorized, headResp.StatusCode,
		"HEAD must bypass auth when GET is declared public (RFC 7231 §4.3.2 alias)")
	assert.Equal(t, int32(0), verifier.callCount.Load(),
		"verifier must not be called for HEAD request to GET-public endpoint")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// B3 WithRelayHealth tests
// ---------------------------------------------------------------------------

func newTestRelay() *runtimeoutbox.Relay {
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
	return runtimeoutbox.NewRelay(outboxtest.NewFakeStore(), &outbox.DiscardPublisher{}, cfg)
}

func TestWithRelayHealth_RegistersCheckers(t *testing.T) {
	ln := newLocalListener(t)

	asm := assembly.New(assembly.Config{ID: "test-relay-health", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	relay := newTestRelay()

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithRelayHealth(relay),
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

	body := decodeSuccessBody(t, resp)
	deps, ok := body["dependencies"].(map[string]any)
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

func TestWithRelayHealth_NilRelay_FailsFast(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-relay-nil", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithRelayHealth(nil),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relay")
}

// bootstrapFailingStore wraps outboxtest.FakeStore to inject a controllable
// ClaimPending error, enabling budget-trip testing in bootstrap integration tests.
type bootstrapFailingStore struct {
	*outboxtest.FakeStore
	mu       sync.Mutex
	claimErr error
}

func (s *bootstrapFailingStore) setClaimErr(err error) {
	s.mu.Lock()
	s.claimErr = err
	s.mu.Unlock()
}

func (s *bootstrapFailingStore) ClaimPending(ctx context.Context, batchSize int) ([]runtimeoutbox.ClaimedEntry, error) {
	s.mu.Lock()
	err := s.claimErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.FakeStore.ClaimPending(ctx, batchSize)
}

// TestWithRelayHealth_TrippedBudget_Returns503 verifies the P1-15 core contract:
// poll budget trip → /readyz returns 503; store recovery → /readyz returns 200.
func TestWithRelayHealth_TrippedBudget_Returns503(t *testing.T) {
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "test-relay-trip", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	store := &bootstrapFailingStore{FakeStore: outboxtest.NewFakeStore()}
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
		PollFailureBudget:    3, // small threshold for fast test
		ReclaimFailureBudget: 3,
		CleanupFailureBudget: 3,
	}
	relay := runtimeoutbox.NewRelay(store, &outbox.DiscardPublisher{}, cfg)

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithRelayHealth(relay),
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

	// Start the relay so its poll loop drives the failure budget.
	relayCtx, relayCancel := context.WithCancel(ctx)
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Start(relayCtx) }()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = relay.Stop(stopCtx)
		relayCancel()
		<-relayDone
	}()

	// Phase 1: store is failing — budget must trip and /readyz must return 503.
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
	code, details := decodeErrorDetails(t, verboseResp)
	assert.Equal(t, "ERR_READYZ_UNHEALTHY", code)
	deps, ok := details["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map in error details")
	require.Contains(t, deps, "outbox-relay-poll", "poll checker must appear in verbose output")
	pollProbe, ok := deps["outbox-relay-poll"].(map[string]any)
	require.True(t, ok, "outbox-relay-poll must be a structured ProbeResult")
	assert.Equal(t, "unhealthy", pollProbe["status"], "outbox-relay-poll: status must be unhealthy")

	// Phase 2: store recovers — budget must reset and /readyz must return 200.
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

func TestWithRelayHealth_DisabledBudget_SkipsChecker(t *testing.T) {
	ln := newLocalListener(t)
	asm := assembly.New(assembly.Config{ID: "test-relay-disabled", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	// Build a relay with poll budget disabled (=0), others enabled.
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
	relay := runtimeoutbox.NewRelay(outboxtest.NewFakeStore(), &outbox.DiscardPublisher{}, cfg)

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithRelayHealth(relay),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	waitForHealthy(t, addr)

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz?verbose", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	body := decodeSuccessBody(t, resp)
	deps, _ := body["dependencies"].(map[string]any)

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

func TestBootstrap_WithLifecycleHook_RunsDuringStart(t *testing.T) {
	var startCalled, stopCalled atomic.Bool

	asm := assembly.New(assembly.Config{ID: "test-lc-ok", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("lc-cell-1")))

	ln := newLocalListener(t)
	addr := ln.Addr().String()

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithLifecycle(func(lc Lifecycle) {
			_ = lc.Append(Hook{
				Name: "test-hook",
				OnStart: func(_ context.Context) error {
					startCalled.Store(true)
					return nil
				},
				OnStop: func(_ context.Context) error {
					stopCalled.Store(true)
					return nil
				},
			})
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- b.Run(ctx) }()

	waitForHealthy(t, addr)

	require.True(t, startCalled.Load(), "OnStart should have been called before HTTP server is ready")

	cancel()
	require.NoError(t, <-errCh)
	require.True(t, stopCalled.Load(), "OnStop should have been called during teardown")
}

func TestBootstrap_WithLifecycleHook_StartFailureHaltsRun(t *testing.T) {
	wantErr := errors.New("hook-boom")

	asm := assembly.New(assembly.Config{ID: "test-lc-fail", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("lc-cell-2")))

	ln := newLocalListener(t)

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithLifecycle(func(lc Lifecycle) {
			_ = lc.Append(Hook{
				Name:    "failing",
				OnStart: func(_ context.Context) error { return wantErr },
			})
		}),
	)

	err := b.Run(t.Context())
	require.Error(t, err)
	require.ErrorIs(t, err, wantErr)
}

// ---------------------------------------------------------------------------
// F7: WithManagedCloser — LIFO teardown registration
// ---------------------------------------------------------------------------

// mockContextCloser is a test double that records Close calls and optionally
// returns an error.
type mockContextCloser struct {
	closed   atomic.Bool
	closeErr error
}

func (m *mockContextCloser) Close(_ context.Context) error {
	m.closed.Store(true)
	return m.closeErr
}

// TestBootstrap_WithManagedCloser_RegistersAsTeardown verifies that a resource
// registered via WithManagedCloser has its Close(ctx) called during shutdown.
//
// ref: uber-go/fx Lifecycle.Append OnStop(ctx) — managed teardown registration.
func TestBootstrap_WithManagedCloser_RegistersAsTeardown(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-managed-closer", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("managed-cell")))

	ln := newLocalListener(t)

	resource := &mockContextCloser{}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

	b := newTestBootstrap(
		WithAssembly(asm),
		WithPrimaryListener(ln),
		WithInternalListener(newLocalListener(t)),
		WithShutdownTimeout(2*time.Second),
		WithManagedCloser(resource),
	)
	go func() { errCh <- b.Run(ctx) }()

	waitForHealthy(t, ln.Addr().String())
	cancel()
	require.NoError(t, <-errCh)

	assert.True(t, resource.closed.Load(), "WithManagedCloser resource must be closed during teardown")
}

// TestBootstrap_WithManagedCloser_NilIgnored verifies that a nil ContextCloser
// is silently ignored without panic.
func TestBootstrap_WithManagedCloser_NilIgnored(t *testing.T) {
	// Passing a nil ContextCloser must not panic at option-construction time.
	assert.NotPanics(t, func() {
		_ = New(WithManagedCloser(nil))
	}, "WithManagedCloser(nil) must not panic")
}

// ---------------------------------------------------------------------------
// F8: FinalizeAuth failure propagates rollback — duplicate auth declaration
// ---------------------------------------------------------------------------

// duplicateAuthCell declares the same (method, path) twice via auth.Mount,
// which must cause FinalizeAuth to return a "duplicate auth declaration" error.
type duplicateAuthCell struct {
	*cell.BaseCell
}

func newDuplicateAuthCell(id string) *duplicateAuthCell {
	return &duplicateAuthCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
	}
}

func (c *duplicateAuthCell) RegisterRoutes(mux cell.RouteMux) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	auth.Mount(mux, auth.Route{Contract: testHTTPContract("GET", "/api/v1/dup"), Handler: handler, Public: true})
	// Declare the same (method, path) a second time — must trigger FinalizeAuth error.
	auth.Mount(mux, auth.Route{Contract: testHTTPContract("GET", "/api/v1/dup"), Handler: handler, Public: true})
}

type protectedAuthCell struct {
	*cell.BaseCell
}

func newProtectedAuthCell(id string) *protectedAuthCell {
	return &protectedAuthCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
	}
}

func (c *protectedAuthCell) RegisterRoutes(mux cell.RouteMux) {
	auth.Mount(mux, auth.Route{Contract: testHTTPContract("GET", "/api/v1/protected"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), Policy: auth.Authenticated()})
}

func TestBootstrap_Phase5_ProtectedRoutesWithoutVerifierFailFast(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-protected-auth", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newProtectedAuthCell("protected-auth-cell")))

	b := newTestBootstrap(
		WithAssembly(asm),
		WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err, "Bootstrap.Run must reject protected route declarations without an auth verifier")
	assert.Contains(t, err.Error(), "auth verifier required")
	assert.Contains(t, err.Error(), "GET /api/v1/protected")
	assert.Contains(t, err.Error(), "WithAuthMiddleware")
	assert.Contains(t, err.Error(), "WithAuthDiscovery")
}

func TestBootstrap_Phase5_FinalizeAuthError_PropagatesRollback(t *testing.T) {
	// F8: a cell that declares the same (method, path) twice causes FinalizeAuth
	// to fail. Bootstrap.Run must propagate the error and roll back (stop the
	// assembly). No HTTP listener must be started.
	asm := assembly.New(assembly.Config{ID: "test-dup-auth", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newDuplicateAuthCell("dup-cell")))

	b := newTestBootstrap(
		WithAssembly(asm),
		// No WithListener: if phase5 errors correctly the listener is never reached.
		// Use a short timeout so the test exits fast if something hangs.
		WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err, "Bootstrap.Run must return error when FinalizeAuth fails")
	assert.Contains(t, err.Error(), "duplicate auth declaration",
		"error must identify the duplicate declaration")

	// After rollback, the assembly cells must be stopped (unhealthy).
	h := asm.Health()
	for id, status := range h {
		assert.Equal(t, "unhealthy", status.Status,
			"cell %s must be unhealthy after rollback", id)
	}
}
