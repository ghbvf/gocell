package websocket_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	adapterws "github.com/ghbvf/gocell/adapters/websocket"
	"github.com/ghbvf/gocell/pkg/errcode"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var _ = os.Exit // suppress unused import

func setupTestHub(t *testing.T, handler rtws.MessageHandler) (*rtws.Hub, *httptest.Server) {
	t.Helper()

	cfg := rtws.DefaultHubConfig()
	cfg.PingInterval = 100 * time.Millisecond

	hub := rtws.NewHub(cfg, handler)

	// Check TCP availability before starting anything.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: cannot listen on TCP (sandbox?): %v", err)
		return nil, nil
	}
	ln.Close()

	// Start hub in background (Register requires running state).
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()

	// Wait for hub to be running before creating server.
	require.Eventually(t, func() bool {
		return hub.IsRunning()
	}, 2*time.Second, time.Millisecond)

	mux := http.NewServeMux()
	// Use explicit AllowedOrigins; empty origins will be rejected after SEC-FAIL-CLOSED-04.
	mux.Handle("/ws", requireUpgradeHandler(t, hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
	}))

	server := httptest.NewServer(mux)

	t.Cleanup(func() {
		server.Close()
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Stop(stopCtx)
		<-startErr
	})

	return hub, server
}

func requireUpgradeHandler(t testing.TB, hub *rtws.Hub, cfg adapterws.UpgradeConfig) http.Handler {
	t.Helper()
	handler, err := adapterws.UpgradeHandler(hub, cfg)
	require.NoError(t, err)
	return handler
}

// dialWS opens a WebSocket connection with an explicit allowed Origin
// header so handshake actually exercises coder/websocket's OriginPatterns
// matching path. Without an Origin header, coder/websocket treats the
// request as same-host and skips the OriginPatterns check entirely —
// silently bypassing the security boundary the tests are meant to cover.
// All setupTestHub-configured AllowedOrigins ("http://*") match the
// header below, so handshake succeeds; tests that assert deny semantics
// dial directly with a custom Origin header instead of using this helper.
func dialWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://example.com"}},
	})
	require.NoError(t, err)

	// Always CloseNow on cleanup. Graceful Close may fail if server
	// already closed the connection, leaving coder/websocket's timeoutLoop alive.
	// CloseNow tears down the transport unconditionally.
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

func TestUpgradeHandler_UpgradeFailureResponseIsPublic(t *testing.T) {
	_, server := setupTestHub(t, nil)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/ws")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "websocket upgrade failed\n", string(body))
	assert.NotContains(t, string(body), "ERR_ADAPTER_WS_UPGRADE")
	assert.NotContains(t, string(body), "websocket: the client is not using the websocket protocol")
}

func TestUpgradeHandler_NonHijackerFailsBeforeAccept(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	cfg.PingInterval = 100 * time.Millisecond
	hub := rtws.NewHub(cfg, nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, hub.IsRunning, 2*time.Second, time.Millisecond)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Stop(stopCtx)
		<-startErr
	})

	handler := requireUpgradeHandler(t, hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
	})
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, "websocket upgrade failed\n", rr.Body.String())
	assert.NotEqual(t, http.StatusSwitchingProtocols, rr.Code)
}

func TestHub_RegisterUnregister(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn := dialWS(t, server.URL)
	_ = conn // cleanup via dialWS t.Cleanup

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHub_Broadcast(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)

	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn1 := dialWS(t, server.URL)
	conn2 := dialWS(t, server.URL)

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 2
	}, 2*time.Second, 10*time.Millisecond)

	hub.Broadcast(context.Background(), []byte("hello all"))

	var wg sync.WaitGroup
	wg.Add(2)

	readMsg := func(c *websocket.Conn) {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, data, err := c.Read(ctx)
		if err != nil {
			t.Logf("read error: %v", err)
			return
		}
		mu.Lock()
		received = append(received, string(data))
		mu.Unlock()
	}

	go readMsg(conn1)
	go readMsg(conn2)
	wg.Wait()

	mu.Lock()
	assert.Len(t, received, 2)
	for _, msg := range received {
		assert.Equal(t, "hello all", msg)
	}
	mu.Unlock()
}

func TestHub_MessageHandler(t *testing.T) {
	var (
		mu         sync.Mutex
		gotConnID  string
		gotMessage string
	)

	handler := func(_ context.Context, connID string, data []byte) {
		mu.Lock()
		gotConnID = connID
		gotMessage = string(data)
		mu.Unlock()
	}

	_, server := setupTestHub(t, handler)
	defer server.Close()

	conn := dialWS(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := conn.Write(ctx, websocket.MessageText, []byte("test message"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMessage != ""
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.NotEmpty(t, gotConnID)
	assert.Equal(t, "test message", gotMessage)
	mu.Unlock()
}

func TestHub_Send(t *testing.T) {
	var (
		mu     sync.Mutex
		connID string
	)

	handler := func(_ context.Context, id string, _ []byte) {
		mu.Lock()
		connID = id
		mu.Unlock()
	}

	hub, server := setupTestHub(t, handler)
	defer server.Close()

	conn := dialWS(t, server.URL)

	// Send a message from client so handler captures connID.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := conn.Write(ctx, websocket.MessageText, []byte("hello"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return connID != ""
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	id := connID
	mu.Unlock()

	// Send a targeted message from hub to client.
	err = hub.Send(context.Background(), id, []byte("direct msg"))
	require.NoError(t, err)

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()

	_, data, err := conn.Read(readCtx)
	require.NoError(t, err)
	assert.Equal(t, "direct msg", string(data))
}

func TestHub_Send_NotFound(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	err := hub.Send(context.Background(), "nonexistent", []byte("test"))
	require.Error(t, err)
}

func TestHub_StopClosesConnections(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn := dialWS(t, server.URL)

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Stop should close the connection and return before timeout.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	err := hub.Stop(stopCtx)
	require.NoError(t, err)

	assert.Equal(t, 0, hub.ConnCount())

	// Client should get a read error (connection closed).
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	_, _, readErr := conn.Read(readCtx)
	assert.Error(t, readErr)
}

func TestDefaultHubConfig(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	assert.Equal(t, 30*time.Second, cfg.PingInterval)
	assert.Equal(t, 5*time.Second, cfg.PingTimeout)
	assert.Equal(t, int64(64*1024), cfg.ReadLimit)
	assert.Equal(t, 2, cfg.PingMissMax)
}

func TestUpgradeHandler_AllowedOrigins(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	hub := rtws.NewHub(cfg, nil)

	handler := requireUpgradeHandler(t, hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
	})

	assert.NotNil(t, handler)
}

func TestHub_FullLifecycle(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	// Connect.
	conn := dialWS(t, server.URL)
	require.Eventually(t, func() bool {
		return hub.ConnCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Broadcast.
	hub.Broadcast(context.Background(), []byte("lifecycle"))
	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	_, data, err := conn.Read(readCtx)
	require.NoError(t, err)
	assert.Equal(t, "lifecycle", string(data))

	// Stop should close connection.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, hub.Stop(stopCtx))

	// Client should see close.
	_, _, readErr := conn.Read(context.Background())
	assert.Error(t, readErr)
}

func TestHub_StopWithActiveConns_NoDeadlock(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	// Connect 3 clients.
	conns := make([]*websocket.Conn, 3)
	for i := range conns {
		conns[i] = dialWS(t, server.URL)
	}
	require.Eventually(t, func() bool {
		return hub.ConnCount() == 3
	}, 2*time.Second, 10*time.Millisecond)

	// Stop must return before timeout (validates CloseNow fix).
	start := time.Now()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	err := hub.Stop(stopCtx)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 2*time.Second, "Stop should not deadlock with active connections")
}

func TestUpgradeHandler_HubNotRunning_503(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	hub := rtws.NewHub(cfg, nil)
	// Hub intentionally NOT started.

	// AllowedOrigins is required post-SEC-FAIL-CLOSED-04; use a valid value.
	handler := requireUpgradeHandler(t, hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
	})

	// Use ResponseRecorder — no TCP needed, no sandbox issue.
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestUpgradeHandler_RejectsEmptyOrigins verifies that constructing an
// UpgradeConfig with nil AllowedOrigins returns an *errcode.Error
// (SEC-FAIL-CLOSED).
//
// Positive case: AllowedOrigins: []string{"http://*"} must not panic.
func TestUpgradeHandler_RejectsEmptyOrigins(t *testing.T) {
	cfg := rtws.DefaultHubConfig()

	t.Run("empty origins — expect construction error with *errcode.Error", func(t *testing.T) {
		hub := rtws.NewHub(cfg, nil)

		handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{AllowedOrigins: nil})
		require.Error(t, err)
		assert.Nil(t, handler)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		if ec.Code != errcode.ErrWebsocketOriginsMissing {
			t.Errorf("error code must be ErrWebsocketOriginsMissing; got %q", ec.Code)
		}
	})

	t.Run("explicit allowed origins — ok", func(t *testing.T) {
		hub := rtws.NewHub(cfg, nil)

		handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
			AllowedOrigins: []string{"http://*"},
		})
		require.NoError(t, err)
		require.NotNil(t, handler)
	})
}

// TestUpgradeHandler_RejectsNilHub verifies that constructing UpgradeHandler
// with a nil *rtws.Hub fails fast at composition time with
// ErrWebsocketHubMissing rather than deferring the failure to the first
// HTTP request — error-first construction contract (PR-MODE-6.1).
func TestUpgradeHandler_RejectsNilHub(t *testing.T) {
	handler, err := adapterws.UpgradeHandler(nil, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
	})

	require.Error(t, err)
	assert.Nil(t, handler)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrWebsocketHubMissing, ec.Code)
}

// TestMustUpgradeHandler_PanicsOnNilHub locks the static-wiring twin: a nil
// hub must surface as a panic at composition root, not at request time, and
// the panic value must be a typed *errcode.Error carrying ErrWebsocketHubMissing
// so a recovery-based composition root can report the precise misconfiguration.
func TestMustUpgradeHandler_PanicsOnNilHub(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "MustUpgradeHandler must panic on nil hub")
		err, ok := r.(error)
		require.True(t, ok, "panic value must be an error, got %T", r)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrWebsocketHubMissing, ec.Code)
	}()
	_ = adapterws.MustUpgradeHandler(nil, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
	})
	t.Fatal("expected MustUpgradeHandler to panic, got none")
}

// TestUpgradeHandler_NilHubTakesPriorityOverOrigins locks the diagnostic order:
// when both hub and cfg are invalid, the caller sees ErrWebsocketHubMissing
// first. Reordering the checks in UpgradeHandler would silently change which
// errcode operators see for misconfigured wiring.
func TestUpgradeHandler_NilHubTakesPriorityOverOrigins(t *testing.T) {
	handler, err := adapterws.UpgradeHandler(nil, adapterws.UpgradeConfig{
		AllowedOrigins: nil,
	})

	require.Error(t, err)
	assert.Nil(t, handler)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrWebsocketHubMissing, ec.Code,
		"nil hub must be diagnosed before invalid origins")
}

func TestUpgradeHandler_RejectsWildcardOrigin(t *testing.T) {
	hub := rtws.NewHub(rtws.DefaultHubConfig(), nil)

	handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"*"},
	})
	require.Error(t, err)
	assert.Nil(t, handler)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrWebsocketOriginsInvalid, ec.Code)
}

func TestMustUpgradeHandler_PanicsOnInvalidConfig(t *testing.T) {
	hub := rtws.NewHub(rtws.DefaultHubConfig(), nil)

	require.Panics(t, func() {
		_ = adapterws.MustUpgradeHandler(hub, adapterws.UpgradeConfig{
			AllowedOrigins: []string{"*"},
		})
	})
}

// TestUpgradeHandler_RejectsBareHostOrigin locks the construction-time
// rejection of host-only patterns. coder/websocket's OriginPatterns
// matches the request's Origin header (which always carries a scheme), so
// a bare host like "example.com" would never match any real browser
// handshake and would silently disable origin checking. Validate must
// surface this as ErrWebsocketOriginsInvalid.
func TestUpgradeHandler_RejectsBareHostOrigin(t *testing.T) {
	hub := rtws.NewHub(rtws.DefaultHubConfig(), nil)

	handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"example.com"},
	})
	require.Error(t, err)
	assert.Nil(t, handler)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrWebsocketOriginsInvalid, ec.Code)
	assert.Contains(t, ec.Error(), "scheme",
		"error message must steer operators to the required pattern shape (origin pattern with scheme)")
	assert.Contains(t, ec.Error(), "example.com",
		"error must echo the offending entry so operators can find it in their config")
}

// dialWithOrigin opens a WebSocket connection with the supplied Origin
// header. Returns the transport-level handshake response (or error). Used
// by the allow/deny black-box tests that assert handshake outcome based on
// the OriginPatterns match — distinct from dialWS which always succeeds.
func dialWithOrigin(t *testing.T, serverURL, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {origin}},
	})
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return conn, resp, err
}

// TestUpgradeHandler_AllowedOrigin_HandshakeSucceeds proves the positive
// branch of the post-coder/websocket-migration origin contract: an Origin
// header that matches the configured pattern completes the handshake and
// registers a connection. Without this assertion, the migration could
// silently weaken or invert allow-policy and the existing suite (which
// dials with no Origin header) would not catch it.
func TestUpgradeHandler_AllowedOrigin_HandshakeSucceeds(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn, _, err := dialWithOrigin(t, server.URL, "http://allowed.example.com")
	require.NoError(t, err, "handshake with allowed Origin must succeed")
	require.NotNil(t, conn)

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 1
	}, 2*time.Second, 10*time.Millisecond,
		"hub must register a client after a successful handshake")
}

// TestUpgradeHandler_DisallowedOrigin_HandshakeRejected proves the
// negative branch: an Origin header that does not match the configured
// pattern is rejected at handshake time and the hub does not register
// the connection. Pairs with the allow test above to lock both directions
// of the security boundary.
func TestUpgradeHandler_DisallowedOrigin_HandshakeRejected(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	cfg.PingInterval = 100 * time.Millisecond
	hub := rtws.NewHub(cfg, nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, hub.IsRunning, 2*time.Second, time.Millisecond)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Stop(stopCtx)
		<-startErr
	})

	mux := http.NewServeMux()
	// Narrow allow-list: only http://*.allowed.test is permitted.
	mux.Handle("/ws", requireUpgradeHandler(t, hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*.allowed.test"},
	}))
	server := httptest.NewServer(mux)
	defer server.Close()

	_, resp, err := dialWithOrigin(t, server.URL, "http://evil.example.com")
	require.Error(t, err, "handshake with disallowed Origin must fail")
	if resp != nil {
		assert.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode,
			"disallowed Origin must not complete the WebSocket upgrade")
		_ = resp.Body.Close()
	}
	assert.Equal(t, 0, hub.ConnCount(),
		"hub must not register a connection rejected at handshake")
}
