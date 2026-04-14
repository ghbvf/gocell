package websocket_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	adapterws "github.com/ghbvf/gocell/adapters/websocket"
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
	mux.Handle("/ws", adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{}))

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

func dialWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)

	// Always CloseNow on cleanup. Graceful Close may fail if server
	// already closed the connection, leaving nhooyr's timeoutLoop alive.
	// CloseNow tears down the transport unconditionally.
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
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

	handler := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"example.com"},
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

	handler := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{})

	// Use ResponseRecorder — no TCP needed, no sandbox issue.
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
