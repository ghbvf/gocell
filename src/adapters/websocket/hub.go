package websocket

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

const (
	// defaultPingInterval is the default interval between ping frames.
	defaultPingInterval = 30 * time.Second
	// defaultWriteTimeout is the default timeout for write operations.
	defaultWriteTimeout = 10 * time.Second
	// defaultReadLimit is the default maximum message size in bytes.
	defaultReadLimit = 64 * 1024 // 64 KB
)

// Conn wraps a WebSocket connection with metadata.
type Conn struct {
	ID   string
	conn *websocket.Conn

	mu     sync.Mutex
	closed bool
}

// Write sends a text message to the connection.
func (c *Conn) Write(ctx context.Context, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errcode.New(ErrAdapterWSClosed, "websocket: connection is closed")
	}

	writeCtx, cancel := context.WithTimeout(ctx, defaultWriteTimeout)
	defer cancel()

	if err := c.conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return errcode.Wrap(ErrAdapterWSWrite, "websocket: write failed", err)
	}
	return nil
}

// Close closes the WebSocket connection gracefully.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close(websocket.StatusNormalClosure, "closing")
}

// HubConfig configures the Hub.
type HubConfig struct {
	// PingInterval is the interval between ping frames. Default: 30s.
	PingInterval time.Duration
	// ReadLimit is the maximum message size in bytes. Default: 64KB.
	ReadLimit int64
}

// DefaultHubConfig returns a HubConfig with sensible defaults.
func DefaultHubConfig() HubConfig {
	return HubConfig{
		PingInterval: defaultPingInterval,
		ReadLimit:    defaultReadLimit,
	}
}

// MessageHandler is called when a message is received from a client.
type MessageHandler func(ctx context.Context, connID string, data []byte)

// Hub manages WebSocket connections and provides signal-first broadcasting.
// In signal-first mode, the Hub pushes messages to all connected clients
// rather than waiting for clients to pull.
type Hub struct {
	config  HubConfig
	handler MessageHandler

	mu    sync.RWMutex
	conns map[string]*Conn

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewHub creates a Hub with the given configuration and message handler.
func NewHub(cfg HubConfig, handler MessageHandler) *Hub {
	if cfg.PingInterval == 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.ReadLimit == 0 {
		cfg.ReadLimit = defaultReadLimit
	}

	return &Hub{
		config:  cfg,
		handler: handler,
		conns:   make(map[string]*Conn),
	}
}

// Start begins the Hub's ping loop. It blocks until ctx is cancelled.
func (h *Hub) Start(ctx context.Context) error {
	ctx, h.cancel = context.WithCancel(ctx)

	slog.Info("websocket hub: started",
		slog.Duration("ping_interval", h.config.PingInterval),
	)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.pingLoop(ctx)
	}()

	<-ctx.Done()
	return ctx.Err()
}

// Stop gracefully shuts down the Hub, closing all connections.
func (h *Hub) Stop(_ context.Context) error {
	h.once.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})

	h.wg.Wait()

	h.mu.Lock()
	defer h.mu.Unlock()

	for connID, c := range h.conns {
		if err := c.Close(); err != nil {
			slog.Warn("websocket hub: close connection failed",
				slog.String("conn_id", connID),
				slog.Any("error", err),
			)
		}
		delete(h.conns, connID)
	}

	slog.Info("websocket hub: stopped")
	return nil
}

// Register adds a WebSocket connection to the Hub and starts reading from it.
func (h *Hub) Register(ctx context.Context, wsConn *websocket.Conn) string {
	connID := "ws" + "-" + uuid.NewString()
	conn := &Conn{
		ID:   connID,
		conn: wsConn,
	}

	wsConn.SetReadLimit(h.config.ReadLimit)

	h.mu.Lock()
	h.conns[connID] = conn
	h.mu.Unlock()

	slog.Info("websocket hub: connection registered",
		slog.String("conn_id", connID),
	)

	// Start reading in a goroutine.
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.readLoop(ctx, conn)
		h.Unregister(connID)
	}()

	return connID
}

// Unregister removes a connection from the Hub.
func (h *Hub) Unregister(connID string) {
	h.mu.Lock()
	conn, ok := h.conns[connID]
	if ok {
		delete(h.conns, connID)
	}
	h.mu.Unlock()

	if ok {
		if err := conn.Close(); err != nil {
			slog.Debug("websocket hub: close on unregister",
				slog.String("conn_id", connID),
				slog.Any("error", err),
			)
		}
		slog.Info("websocket hub: connection unregistered",
			slog.String("conn_id", connID),
		)
	}
}

// Broadcast sends a text message to all connected clients (signal-first mode).
func (h *Hub) Broadcast(ctx context.Context, data []byte) {
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, c := range conns {
		if err := c.Write(ctx, data); err != nil {
			slog.Warn("websocket hub: broadcast write failed",
				slog.String("conn_id", c.ID),
				slog.Any("error", err),
			)
		}
	}
}

// Send sends a text message to a specific connection.
func (h *Hub) Send(ctx context.Context, connID string, data []byte) error {
	h.mu.RLock()
	conn, ok := h.conns[connID]
	h.mu.RUnlock()

	if !ok {
		return errcode.New(ErrAdapterWSClosed,
			"websocket: connection not found: "+connID)
	}

	return conn.Write(ctx, data)
}

// ConnCount returns the number of active connections.
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// readLoop reads messages from a connection and dispatches to the handler.
func (h *Hub) readLoop(ctx context.Context, conn *Conn) {
	for {
		_, data, err := conn.conn.Read(ctx)
		if err != nil {
			// Normal closure or context cancel are expected.
			slog.Debug("websocket hub: read loop ended",
				slog.String("conn_id", conn.ID),
				slog.Any("error", err),
			)
			return
		}

		if h.handler != nil {
			h.handler(ctx, conn.ID, data)
		}
	}
}

// pingLoop periodically pings all connections to detect stale ones.
func (h *Hub) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.pingAll(ctx)
		}
	}
}

// pingAll pings every connection and removes unresponsive ones.
func (h *Hub) pingAll(ctx context.Context) {
	h.mu.RLock()
	conns := make(map[string]*Conn, len(h.conns))
	for k, v := range h.conns {
		conns[k] = v
	}
	h.mu.RUnlock()

	for connID, c := range conns {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := c.conn.Ping(pingCtx); err != nil {
			slog.Warn("websocket hub: ping failed, removing connection",
				slog.String("conn_id", connID),
				slog.Any("error", err),
			)
			h.Unregister(connID)
		}
		cancel()
	}
}
