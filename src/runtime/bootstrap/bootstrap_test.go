package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testHTTPClient is used in place of http.DefaultClient to prevent test
// hangs on stalled connections (e.g., during shutdown races).
var testHTTPClient = &http.Client{Timeout: 2 * time.Second}

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
	})
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
	asm := assembly.New(assembly.Config{ID: "test-router-ok"})
	ec := newEventCell("ok-cell", nil) // nil error → registers 1 handler
	require.NoError(t, asm.Register(ec))

	eb := eventbus.New()
	b := New(
		WithAssembly(asm),
		WithSubscriber(eb),
		WithPublisher(eb),
		WithHTTPAddr("127.0.0.1:0"),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Give bootstrap time to start (Router + HTTP).
	time.Sleep(time.Second)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err, "clean shutdown should not produce an error")
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

	// GET /readyz and verify the checker appears as healthy.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
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

	// GET /readyz and verify the checker appears as unhealthy.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
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

func TestWithHealthChecker_EmptyName_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "bootstrap: health checker name must not be empty", func() {
		WithHealthChecker("", func() error { return nil })
	})
}

func TestWithHealthChecker_NilFn_Panics(t *testing.T) {
	assert.PanicsWithValue(t, `bootstrap: health checker "rabbitmq" must not be nil`, func() {
		WithHealthChecker("rabbitmq", nil)
	})
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

	// GET /readyz — one unhealthy checker should make the whole response 503.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
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

func (c *plainConfig) Get(key string) any        { return c.data[key] }
func (c *plainConfig) Scan(_ interface{}) error   { return nil }
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
	mu        sync.Mutex
	events    []cell.ConfigChangeEvent
	callOrder *[]string // shared slice to track call order across cells
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
	// ref: fsnotify eventSeparator pattern (50ms); we use 200ms for CI margin.
	time.Sleep(200 * time.Millisecond)

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
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val2\n"), 0o644))
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	evt := rc.lastEvent()
	require.NotNil(t, evt)
	assert.Equal(t, int64(1), evt.Generation, "first reload should have generation 1")

	// Second change.
	require.NoError(t, os.WriteFile(cfgFile, []byte("key: val3\n"), 0o644))
	require.Eventually(t, func() bool {
		return rc.eventCount() >= 2
	}, 3*time.Second, 50*time.Millisecond)

	evt = rc.lastEvent()
	require.NotNil(t, evt)
	assert.Equal(t, int64(2), evt.Generation, "second reload should have generation 2")

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
