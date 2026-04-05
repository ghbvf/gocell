package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"nhooyr.io/websocket"
)

// Error codes for the WebSocket adapter.
const (
	ErrAdapterWSUpgrade errcode.Code = "ERR_ADAPTER_WS_UPGRADE"
	ErrAdapterWSSend    errcode.Code = "ERR_ADAPTER_WS_SEND"
	ErrAdapterWSClosed  errcode.Code = "ERR_ADAPTER_WS_CLOSED"
)

// DefaultPingInterval is the default interval between server-sent ping frames.
const DefaultPingInterval = 30 * time.Second

// DefaultPongTimeout is the default time allowed for a pong response before
// the connection is considered dead.
const DefaultPongTimeout = 10 * time.Second

// defaultSendBuffer is the channel buffer size per connection. A slow consumer
// whose buffer fills up is closed rather than blocking the broadcaster.
const defaultSendBuffer = 16

// HubConfig holds tuneable parameters for a Hub.
type HubConfig struct {
	PingInterval time.Duration
	PongTimeout  time.Duration
	SendBuffer   int
}

// applyDefaults fills zero-value fields with sensible defaults.
func (c *HubConfig) applyDefaults() {
	if c.PingInterval <= 0 {
		c.PingInterval = DefaultPingInterval
	}
	if c.PongTimeout <= 0 {
		c.PongTimeout = DefaultPongTimeout
	}
	if c.SendBuffer <= 0 {
		c.SendBuffer = defaultSendBuffer
	}
}

// conn represents a single WebSocket connection registered with the Hub.
type conn struct {
	id     string
	userID string
	ws     *websocket.Conn
	msgs   chan []byte
}

// Hub manages a set of WebSocket connections and provides broadcast / unicast
// delivery of lightweight signal messages.
type Hub struct {
	cfg   HubConfig
	mu    sync.Mutex
	conns map[*conn]struct{}
}

// NewHub creates a Hub with the given configuration. Nil-safe: passing a nil
// pointer uses all defaults.
func NewHub(cfg *HubConfig) *Hub {
	var c HubConfig
	if cfg != nil {
		c = *cfg
	}
	c.applyDefaults()
	return &Hub{
		cfg:   c,
		conns: make(map[*conn]struct{}),
	}
}

// register adds a connection to the Hub.
func (h *Hub) register(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
	slog.Info("websocket: connection registered",
		"connection_id", c.id,
		"user_id", c.userID,
	)
}

// unregister removes a connection from the Hub.
func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; ok {
		delete(h.conns, c)
		close(c.msgs)
		slog.Info("websocket: connection unregistered",
			"connection_id", c.id,
			"user_id", c.userID,
		)
	}
}

// Broadcast sends a message to every connected client. Connections whose send
// buffer is full are closed asynchronously (slow-consumer protection, adapted
// from nhooyr.io/websocket chat example).
func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for c := range h.conns {
		select {
		case c.msgs <- msg:
		default:
			go h.closeSlow(c)
		}
	}
}

// Unicast sends a message to a specific connection identified by connectionID.
// Returns an error if the connection is not found.
func (h *Hub) Unicast(connectionID string, msg []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	for c := range h.conns {
		if c.id == connectionID {
			select {
			case c.msgs <- msg:
				return nil
			default:
				go h.closeSlow(c)
				return errcode.New(ErrAdapterWSClosed, "connection send buffer full, closing: "+connectionID)
			}
		}
	}
	return errcode.New(ErrAdapterWSClosed, "connection not found: "+connectionID)
}

// SendToUser sends a message to all connections belonging to the given userID.
func (h *Hub) SendToUser(userID string, msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for c := range h.conns {
		if c.userID == userID {
			select {
			case c.msgs <- msg:
			default:
				go h.closeSlow(c)
			}
		}
	}
}

// ConnCount returns the number of active connections (for observability).
func (h *Hub) ConnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// closeSlow terminates a slow consumer.
func (h *Hub) closeSlow(c *conn) {
	slog.Warn("websocket: closing slow consumer",
		"connection_id", c.id,
		"user_id", c.userID,
	)
	c.ws.Close(websocket.StatusPolicyViolation, "slow consumer")
}

// writeLoop drains the outbound message queue for a connection, handling
// heartbeat pings and write timeouts.
func (h *Hub) writeLoop(ctx context.Context, c *conn) {
	ticker := time.NewTicker(h.cfg.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-c.msgs:
			if !ok {
				// Channel closed — connection was unregistered.
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, h.cfg.PongTimeout)
			err := c.ws.Write(writeCtx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				slog.Error("websocket: write failed",
					"error", err,
					"connection_id", c.id,
					"user_id", c.userID,
				)
				return
			}

		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, h.cfg.PongTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				slog.Warn("websocket: ping/pong timeout",
					"error", err,
					"connection_id", c.id,
					"user_id", c.userID,
				)
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// serve runs the read and write loops for a connection. It blocks until the
// connection is closed.
func (h *Hub) serve(ctx context.Context, c *conn) {
	h.register(c)
	defer h.unregister(c)

	// Write loop in a separate goroutine.
	done := make(chan struct{})
	go func() {
		h.writeLoop(ctx, c)
		close(done)
	}()

	// Read loop — we only read to detect closure and process control frames.
	for {
		_, _, err := c.ws.Read(ctx)
		if err != nil {
			slog.Debug("websocket: read loop ended",
				"connection_id", c.id,
				"reason", fmt.Sprintf("%v", err),
			)
			break
		}
		// Signal-first mode: client messages are ignored.
	}

	// Wait for write loop to finish.
	<-done
}
