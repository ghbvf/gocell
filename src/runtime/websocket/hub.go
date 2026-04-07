package websocket

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	defaultPingInterval = 30 * time.Second
	defaultPingTimeout  = 5 * time.Second
)

// HubConfig configures the Hub.
type HubConfig struct {
	// PingInterval is the interval between ping sweeps. Default: 30s.
	PingInterval time.Duration
	// PingTimeout is the deadline for a single ping. Default: 5s.
	PingTimeout time.Duration
	// ReadLimit is the maximum message size in bytes. Default: 64KB.
	// The adapter applies this when creating a connection.
	ReadLimit int64
}

const defaultReadLimit = 64 * 1024 // 64KB

// DefaultHubConfig returns a HubConfig with sensible defaults.
func DefaultHubConfig() HubConfig {
	return HubConfig{
		PingInterval: defaultPingInterval,
		PingTimeout:  defaultPingTimeout,
		ReadLimit:    defaultReadLimit,
	}
}

// MessageHandler is called when a message is received from a client.
type MessageHandler func(ctx context.Context, connID string, data []byte)

// Hub manages WebSocket connections and provides signal-first broadcasting.
type Hub struct {
	config  HubConfig
	handler MessageHandler

	mu    sync.RWMutex
	conns map[string]Conn

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewHub creates a Hub with the given configuration and message handler.
func NewHub(cfg HubConfig, handler MessageHandler) *Hub {
	if cfg.PingInterval == 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.PingTimeout == 0 {
		cfg.PingTimeout = defaultPingTimeout
	}
	if cfg.ReadLimit == 0 {
		cfg.ReadLimit = defaultReadLimit
	}
	return &Hub{
		config:  cfg,
		handler: handler,
		conns:   make(map[string]Conn),
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

// Register adds a connection to the Hub and starts reading from it.
func (h *Hub) Register(ctx context.Context, conn Conn) {
	h.mu.Lock()
	h.conns[conn.ID()] = conn
	h.mu.Unlock()

	slog.Info("websocket hub: connection registered",
		slog.String("conn_id", conn.ID()),
	)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.readLoop(ctx, conn)
		h.Unregister(conn.ID())
	}()
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
	conns := make([]Conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, c := range conns {
		if err := c.Write(ctx, data); err != nil {
			slog.Warn("websocket hub: broadcast write failed",
				slog.String("conn_id", c.ID()),
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
		return errcode.New(ErrWSConnNotFound,
			"websocket: connection not found: "+connID)
	}

	return conn.Write(ctx, data)
}

// Config returns the Hub's configuration. Adapters use this to read
// settings like ReadLimit when creating connections.
func (h *Hub) Config() HubConfig { return h.config }

// ConnCount returns the number of active connections.
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

func (h *Hub) readLoop(ctx context.Context, conn Conn) {
	for {
		data, err := conn.Read(ctx)
		if err != nil {
			slog.Debug("websocket hub: read loop ended",
				slog.String("conn_id", conn.ID()),
				slog.Any("error", err),
			)
			return
		}
		if h.handler != nil {
			h.handler(ctx, conn.ID(), data)
		}
	}
}

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

func (h *Hub) pingAll(ctx context.Context) {
	h.mu.RLock()
	conns := make(map[string]Conn, len(h.conns))
	for k, v := range h.conns {
		conns[k] = v
	}
	h.mu.RUnlock()

	for connID, c := range conns {
		pingCtx, cancel := context.WithTimeout(ctx, h.config.PingTimeout)
		if err := c.Ping(pingCtx); err != nil {
			slog.Warn("websocket hub: ping failed, removing connection",
				slog.String("conn_id", connID),
				slog.Any("error", err),
			)
			h.Unregister(connID)
		}
		cancel()
	}
}
