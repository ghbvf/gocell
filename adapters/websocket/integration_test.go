//go:build integration

package websocket_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	adapterws "github.com/ghbvf/gocell/adapters/websocket"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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
	cfg := rtws.DefaultHubConfig(clock.Real())
	cfg.PingInterval = testtime.D200ms
	hub := rtws.NewHub(cfg, handler)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()

	require.Eventually(t, func() bool { return hub.IsRunning() }, testtime.D2s, time.Millisecond)

	mux := http.NewServeMux()
	mux.Handle("/ws", requireUpgradeHandler(t, hub, adapterws.UpgradeConfig{
		// SEC-FAIL-CLOSED (PR-MODE-1): empty AllowedOrigins is rejected by
		// Validate. Use an explicit scheme://host pattern so this integration
		// suite exercises coder/websocket's OriginPatterns matcher; bare host
		// would never match the Origin header that dialIntegrationWS sends.
		AllowedOrigins: []string{"http://*"},
	}))
	server := httptest.NewServer(mux)

	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
		defer cancel()
		_ = hub.Stop(ctx)
		<-startErr
	})
	return hub, server
}

// dialIntegrationWS opens a WebSocket connection with an explicit Origin
// header so the integration suite exercises coder/websocket's
// OriginPatterns matching path. Without an Origin header, coder/websocket
// treats the request as same-host and skips OriginPatterns entirely —
// silently bypassing the allow-list.
func dialIntegrationWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://example.com"}},
	})
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

	require.Eventually(t, func() bool { return hub.ConnCount() == 1 }, testtime.D2s, testtime.D10ms)

	// Client sends a message.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("echo hello")))

	// Handler should receive it.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMsg != ""
	}, testtime.D2s, testtime.D10ms)

	mu.Lock()
	assert.Equal(t, "echo hello", gotMsg)
	assert.NotEmpty(t, gotConn)
	mu.Unlock()

	// Hub sends a reply back to the client.
	mu.Lock()
	id := gotConn
	mu.Unlock()
	require.NoError(t, hub.Send(context.Background(), id, []byte("echo reply")))

	readCtx, readCancel := context.WithTimeout(context.Background(), testtime.D2s)
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
	}, testtime.D2s, testtime.D10ms)

	hub.Broadcast(context.Background(), []byte("broadcast msg"))

	var wg sync.WaitGroup
	var mu sync.Mutex
	received := make([]string, 0, numClients)

	for _, c := range conns {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
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
	}, testtime.D2s, testtime.D10ms)

	// Stop hub — should close all connections within deadline.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
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
