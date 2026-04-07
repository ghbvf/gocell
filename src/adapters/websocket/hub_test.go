package websocket_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	adapterws "github.com/ghbvf/gocell/adapters/websocket"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHub(t *testing.T, handler rtws.MessageHandler) (*rtws.Hub, *httptest.Server) {
	t.Helper()

	cfg := rtws.DefaultHubConfig()
	cfg.PingInterval = 100 * time.Millisecond

	hub := rtws.NewHub(cfg, handler)

	mux := http.NewServeMux()
	mux.Handle("/ws", adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: cannot listen on TCP (sandbox?): %v", err)
		return nil, nil
	}
	ln.Close()

	server := httptest.NewServer(mux)
	return hub, server
}

func dialWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	return conn
}

func TestHub_RegisterUnregister(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, hub.ConnCount())
}

func TestHub_Broadcast(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)

	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn1 := dialWS(t, server.URL)
	defer func() {
		if err := conn1.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	conn2 := dialWS(t, server.URL)
	defer func() {
		if err := conn2.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 2, hub.ConnCount())

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
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := conn.Write(ctx, websocket.MessageText, []byte("test message"))
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

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
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

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

func TestHub_StartStop(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	cfg.PingInterval = 50 * time.Millisecond

	hub := rtws.NewHub(cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- hub.Start(ctx)
	}()

	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	err := hub.Stop(stopCtx)
	require.NoError(t, err)

	startErr := <-errCh
	assert.ErrorIs(t, startErr, context.DeadlineExceeded)
}

func TestHub_StopClosesConnections(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn := dialWS(t, server.URL)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, hub.ConnCount())

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
}

func TestUpgradeHandler_AllowedOrigins(t *testing.T) {
	cfg := rtws.DefaultHubConfig()
	hub := rtws.NewHub(cfg, nil)

	handler := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
		AllowedOrigins: []string{"example.com"},
	})

	assert.NotNil(t, handler)
}
