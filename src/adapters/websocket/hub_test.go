package websocket

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHub(t *testing.T, handler MessageHandler) (*Hub, *httptest.Server) {
	t.Helper()

	cfg := DefaultHubConfig()
	cfg.PingInterval = 100 * time.Millisecond

	hub := NewHub(cfg, handler)

	mux := http.NewServeMux()
	mux.Handle("/ws", UpgradeHandler(hub, UpgradeConfig{}))

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

	// Connect a client.
	conn := dialWS(t, server.URL)
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	// Wait for the hub to register the connection.
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

	// Connect two clients.
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

	// Wait for registration.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 2, hub.ConnCount())

	// Broadcast a message.
	hub.Broadcast(context.Background(), []byte("hello all"))

	// Read from both clients.
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
		mu          sync.Mutex
		gotConnID   string
		gotMessage  string
	)

	handler := func(_ context.Context, connID string, data []byte) {
		mu.Lock()
		gotConnID = connID
		gotMessage = string(data)
		mu.Unlock()
	}

	hub, server := setupTestHub(t, handler)
	defer server.Close()
	_ = hub

	conn := dialWS(t, server.URL)
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	// Wait for registration.
	time.Sleep(100 * time.Millisecond)

	// Send a message from client to server.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := conn.Write(ctx, websocket.MessageText, []byte("test message"))
	require.NoError(t, err)

	// Wait for handler to process.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	assert.NotEmpty(t, gotConnID)
	assert.Equal(t, "test message", gotMessage)
	mu.Unlock()
}

func TestHub_Send(t *testing.T) {
	hub, server := setupTestHub(t, nil)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
			t.Logf("close error: %v", err)
		}
	}()

	// Wait for registration.
	time.Sleep(100 * time.Millisecond)

	// Find the connection ID.
	hub.mu.RLock()
	var connID string
	for k := range hub.conns {
		connID = k
		break
	}
	hub.mu.RUnlock()
	require.NotEmpty(t, connID)

	// Send a targeted message.
	err := hub.Send(context.Background(), connID, []byte("direct msg"))
	require.NoError(t, err)

	// Read from client.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
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
	cfg := DefaultHubConfig()
	cfg.PingInterval = 50 * time.Millisecond

	hub := NewHub(cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- hub.Start(ctx)
	}()

	<-ctx.Done()
	err := hub.Stop(context.Background())
	require.NoError(t, err)

	startErr := <-errCh
	assert.ErrorIs(t, startErr, context.DeadlineExceeded)
}

func TestDefaultHubConfig(t *testing.T) {
	cfg := DefaultHubConfig()
	assert.Equal(t, defaultPingInterval, cfg.PingInterval)
	assert.Equal(t, int64(defaultReadLimit), cfg.ReadLimit)
}

func TestUpgradeHandler_AllowedOrigins(t *testing.T) {
	cfg := DefaultHubConfig()
	hub := NewHub(cfg, nil)

	handler := UpgradeHandler(hub, UpgradeConfig{
		AllowedOrigins: []string{"example.com"},
	})

	// Test that the handler is created (actual origin checking is done by nhooyr.io).
	assert.NotNil(t, handler)
}
