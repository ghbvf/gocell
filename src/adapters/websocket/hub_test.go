package websocket

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

func TestHubConfig_applyDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  HubConfig
		want HubConfig
	}{
		{
			name: "all zero values get defaults",
			cfg:  HubConfig{},
			want: HubConfig{
				PingInterval: DefaultPingInterval,
				PongTimeout:  DefaultPongTimeout,
				SendBuffer:   defaultSendBuffer,
			},
		},
		{
			name: "custom values preserved",
			cfg: HubConfig{
				PingInterval: 5 * time.Second,
				PongTimeout:  2 * time.Second,
				SendBuffer:   32,
			},
			want: HubConfig{
				PingInterval: 5 * time.Second,
				PongTimeout:  2 * time.Second,
				SendBuffer:   32,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.applyDefaults()
			assert.Equal(t, tt.want, tt.cfg)
		})
	}
}

func TestNewHub(t *testing.T) {
	t.Run("nil config uses defaults", func(t *testing.T) {
		h := NewHub(nil)
		require.NotNil(t, h)
		assert.Equal(t, DefaultPingInterval, h.cfg.PingInterval)
		assert.Equal(t, DefaultPongTimeout, h.cfg.PongTimeout)
		assert.Equal(t, defaultSendBuffer, h.cfg.SendBuffer)
		assert.Empty(t, h.conns)
	})

	t.Run("custom config", func(t *testing.T) {
		h := NewHub(&HubConfig{PingInterval: 10 * time.Second})
		assert.Equal(t, 10*time.Second, h.cfg.PingInterval)
		assert.Equal(t, DefaultPongTimeout, h.cfg.PongTimeout)
	})
}

func TestHub_RegisterUnregister(t *testing.T) {
	h := NewHub(nil)
	c := &conn{
		id:     "c1",
		userID: "u1",
		msgs:   make(chan []byte, 1),
	}

	h.register(c)
	assert.Equal(t, 1, h.ConnCount())

	h.unregister(c)
	assert.Equal(t, 0, h.ConnCount())
}

func TestHub_UnregisterIdempotent(t *testing.T) {
	h := NewHub(nil)
	c := &conn{
		id:     "c1",
		userID: "u1",
		msgs:   make(chan []byte, 1),
	}

	h.register(c)
	h.unregister(c)
	// Second unregister should not panic.
	h.unregister(c)
	assert.Equal(t, 0, h.ConnCount())
}

func TestHub_Broadcast(t *testing.T) {
	h := NewHub(nil)

	c1 := &conn{id: "c1", userID: "u1", msgs: make(chan []byte, 4)}
	c2 := &conn{id: "c2", userID: "u2", msgs: make(chan []byte, 4)}

	h.register(c1)
	h.register(c2)

	msg := []byte(`{"type":"refresh","resource":"config"}`)
	h.Broadcast(msg)

	got1 := <-c1.msgs
	got2 := <-c2.msgs
	assert.Equal(t, msg, got1)
	assert.Equal(t, msg, got2)
}

func TestHub_Unicast(t *testing.T) {
	h := NewHub(nil)

	c1 := &conn{id: "c1", userID: "u1", msgs: make(chan []byte, 4)}
	c2 := &conn{id: "c2", userID: "u2", msgs: make(chan []byte, 4)}

	h.register(c1)
	h.register(c2)

	msg := []byte(`{"type":"refresh","resource":"session"}`)
	err := h.Unicast("c1", msg)
	require.NoError(t, err)

	got := <-c1.msgs
	assert.Equal(t, msg, got)

	// c2 should have no messages.
	select {
	case <-c2.msgs:
		t.Fatal("c2 should not have received a message")
	default:
	}
}

func TestHub_UnicastNotFound(t *testing.T) {
	h := NewHub(nil)
	err := h.Unicast("nonexistent", []byte("hello"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_WS_CLOSED")
}

func TestHub_SendToUser(t *testing.T) {
	h := NewHub(nil)

	c1 := &conn{id: "c1", userID: "u1", msgs: make(chan []byte, 4)}
	c2 := &conn{id: "c2", userID: "u1", msgs: make(chan []byte, 4)} // same user
	c3 := &conn{id: "c3", userID: "u2", msgs: make(chan []byte, 4)}

	h.register(c1)
	h.register(c2)
	h.register(c3)

	msg := []byte(`{"type":"refresh","resource":"role"}`)
	h.SendToUser("u1", msg)

	got1 := <-c1.msgs
	got2 := <-c2.msgs
	assert.Equal(t, msg, got1)
	assert.Equal(t, msg, got2)

	select {
	case <-c3.msgs:
		t.Fatal("c3 (different user) should not have received a message")
	default:
	}
}

func TestWriteSignal_NilHub(t *testing.T) {
	err := WriteSignal(nil, []byte("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_WS_SEND")
}

// TestUpgradeHandler_Integration performs a full WebSocket handshake and
// verifies that the Hub receives and delivers messages end-to-end.
// Requires network access (httptest.NewServer), skipped when port binding is
// unavailable (e.g. sandboxed CI).
func TestUpgradeHandler_Integration(t *testing.T) {
	// Detect sandbox by attempting a listener.
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Skip("skipping integration test: cannot bind port:", lnErr)
	}
	ln.Close()
	hub := NewHub(&HubConfig{
		PingInterval: 100 * time.Millisecond,
		PongTimeout:  50 * time.Millisecond,
		SendBuffer:   4,
	})

	handler := UpgradeHandler(hub, HandlerConfig{
		UserIDExtractor: func(r *http.Request) string {
			return r.Header.Get("X-User-ID")
		},
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	headers := http.Header{}
	headers.Set("X-User-ID", "test-user")
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	require.NoError(t, err)
	defer ws.CloseNow()

	// Wait for the connection to be registered.
	require.Eventually(t, func() bool {
		return hub.ConnCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Broadcast a message.
	signal := []byte(`{"type":"refresh","resource":"config"}`)
	hub.Broadcast(signal)

	// Read the message from the client side.
	_, msg, err := ws.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, signal, msg)

	// Close the connection and verify unregister.
	ws.Close(websocket.StatusNormalClosure, "done")

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, 2*time.Second, 10*time.Millisecond)
}
