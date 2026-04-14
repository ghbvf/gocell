//go:build integration

package websocket_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	adapterws "github.com/ghbvf/gocell/adapters/websocket"
	rtws "github.com/ghbvf/gocell/runtime/websocket"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"net/http"
	"net/http/httptest"
	"strings"
)

// setupIntegrationHub creates a running Hub + httptest server for integration tests.
func setupIntegrationHub(t *testing.T, handler rtws.MessageHandler) (*rtws.Hub, *httptest.Server) {
	t.Helper()
	cfg := rtws.DefaultHubConfig()
	cfg.PingInterval = 200 * time.Millisecond
	hub := rtws.NewHub(cfg, handler)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()

	require.Eventually(t, func() bool { return hub.IsRunning() }, 2*time.Second, time.Millisecond)

	mux := http.NewServeMux()
	mux.Handle("/ws", adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{}))
	server := httptest.NewServer(mux)

	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Stop(ctx)
		<-startErr
	})
	return hub, server
}

func dialIntegrationWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// TestIntegration_ConnectAndEcho connects a client, sends a message, and
// verifies the Hub delivers it to the MessageHandler.
func TestIntegration_ConnectAndEcho(t *testing.T) {
	var (
		mu      sync.Mutex
		gotMsg  string
		gotConn string
	)
	handler := func(_ context.Context, connID string, data []byte) {
		mu.Lock()
		gotConn = connID
		gotMsg = string(data)
		mu.Unlock()
	}

	hub, server := setupIntegrationHub(t, handler)

	conn := dialIntegrationWS(t, server.URL)
	// cleanup via dialIntegrationWS t.Cleanup

	require.Eventually(t, func() bool { return hub.ConnCount() == 1 }, 2*time.Second, 10*time.Millisecond)

	// Client sends a message.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("echo hello")))

	// Handler should receive it.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMsg != ""
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "echo hello", gotMsg)
	assert.NotEmpty(t, gotConn)
	mu.Unlock()

	// Hub sends a reply back to the client.
	mu.Lock()
	id := gotConn
	mu.Unlock()
	require.NoError(t, hub.Send(context.Background(), id, []byte("echo reply")))

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	_, data, err := conn.Read(readCtx)
	require.NoError(t, err)
	assert.Equal(t, "echo reply", string(data))
}

// TestIntegration_BroadcastMultipleClients connects multiple clients and
// verifies a broadcast message reaches all of them.
func TestIntegration_BroadcastMultipleClients(t *testing.T) {
	hub, server := setupIntegrationHub(t, nil)

	const numClients = 5
	conns := make([]*websocket.Conn, numClients)
	for i := range conns {
		conns[i] = dialIntegrationWS(t, server.URL)
		// cleanup via dialIntegrationWS t.Cleanup
	}

	require.Eventually(t, func() bool {
		return hub.ConnCount() == numClients
	}, 2*time.Second, 10*time.Millisecond)

	hub.Broadcast(context.Background(), []byte("broadcast msg"))

	var wg sync.WaitGroup
	var mu sync.Mutex
	received := make([]string, 0, numClients)

	for _, c := range conns {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			received = append(received, string(data))
			mu.Unlock()
		}(c)
	}
	wg.Wait()

	mu.Lock()
	assert.Len(t, received, numClients)
	for _, msg := range received {
		assert.Equal(t, "broadcast msg", msg)
	}
	mu.Unlock()
}

// TestIntegration_GracefulShutdown shuts down the Hub while clients are
// connected and asserts all connections are closed cleanly.
func TestIntegration_GracefulShutdown(t *testing.T) {
	hub, server := setupIntegrationHub(t, nil)

	const numClients = 3
	conns := make([]*websocket.Conn, numClients)
	for i := range conns {
		conns[i] = dialIntegrationWS(t, server.URL)
	}

	require.Eventually(t, func() bool {
		return hub.ConnCount() == numClients
	}, 2*time.Second, 10*time.Millisecond)

	// Stop hub — should close all connections within deadline.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, hub.Stop(stopCtx))

	assert.Equal(t, 0, hub.ConnCount())

	// All clients should get a read error.
	for _, c := range conns {
		readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err := c.Read(readCtx)
		readCancel()
		assert.Error(t, err, "client should see connection closed")
	}
}
