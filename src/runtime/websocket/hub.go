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
	defaultReadLimit    = 64 * 1024 // 64KB
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
//
// Lifecycle: NewHub → Start (blocks) → Stop → (optionally Start again).
// Start and Stop may be called multiple times in sequence. Double-Start
// without an intervening Stop returns ErrWSLifecycle.
type Hub struct {
	config  HubConfig
	handler MessageHandler

	// connMu guards conns map. wg.Add must happen under connMu to prevent
	// a race between Register and Stop's wg.Wait.
	connMu sync.RWMutex
	conns  map[string]Conn
	wg     sync.WaitGroup

	// stateMu guards started, stopping, startCancel.
	stateMu     sync.Mutex
	started     bool
	stopping    bool
	startCancel context.CancelFunc
}

// NewHub creates a Hub. No background goroutines are started until Start.
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

// Start begins the Hub's ping loop. It blocks until Stop is called or ctx
// is cancelled. Returns nil when stopped normally, ctx.Err() when the
// caller's context expires, or ErrWSLifecycle if already started.
func (h *Hub) Start(ctx context.Context) error {
	h.stateMu.Lock()
	if h.started {
		h.stateMu.Unlock()
		return errcode.New(ErrWSLifecycle, "websocket: hub already started")
	}
	h.started = true
	h.stopping = false
	runCtx, cancel := context.WithCancel(ctx)
	h.startCancel = cancel
	h.stateMu.Unlock()

	slog.Info("websocket hub: started",
		slog.Duration("ping_interval", h.config.PingInterval),
	)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.pingLoop(runCtx)
	}()

	// Block until Stop cancels runCtx or the caller's ctx expires.
	<-runCtx.Done()

	// Distinguish Stop (normal) from external cancellation.
	h.stateMu.Lock()
	stopped := h.stopping
	h.stateMu.Unlock()

	if stopped {
		return nil
	}
	return ctx.Err()
}

// Stop gracefully shuts down the Hub. It rejects new connections, cancels
// the ping loop, closes all connections (breaking readLoops), and waits
// for goroutines to exit. The provided ctx bounds the wait time.
//
// After Stop returns, the Hub may be Start-ed again.
func (h *Hub) Stop(ctx context.Context) error {
	h.stateMu.Lock()
	h.stopping = true
	cancel := h.startCancel
	h.startCancel = nil
	h.stateMu.Unlock()

	// Cancel the run context (stops ping loop, unblocks Start).
	if cancel != nil {
		cancel()
	}

	// Drain and close all connections under lock.
	h.connMu.Lock()
	conns := make([]Conn, 0, len(h.conns))
	for id, c := range h.conns {
		conns = append(conns, c)
		delete(h.conns, id)
	}
	h.connMu.Unlock()

	for _, c := range conns {
		if err := c.Close(); err != nil {
			slog.Warn("websocket hub: close connection failed",
				slog.String("conn_id", c.ID()),
				slog.Any("error", err),
			)
		}
	}

	// Wait for goroutines (readLoops + pingLoop) with deadline.
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()

	var err error
	select {
	case <-done:
		slog.Info("websocket hub: stopped")
	case <-ctx.Done():
		slog.Warn("websocket hub: stop timed out")
		err = ctx.Err()
	}

	// Reset state so Hub can be restarted.
	h.stateMu.Lock()
	h.started = false
	h.stopping = false
	h.stateMu.Unlock()

	return err
}

// Register adds a connection to the Hub and starts reading from it.
// The read loop does not depend on any context — it exits only when
// conn.Close() causes Read to return an error (triggered by Stop or
// Unregister). Returns ErrWSLifecycle if the Hub is shutting down.
func (h *Hub) Register(conn Conn) error {
	h.stateMu.Lock()
	stopping := h.stopping
	h.stateMu.Unlock()

	if stopping {
		_ = conn.Close()
		return errcode.New(ErrWSLifecycle, "websocket: hub is stopping, connection rejected")
	}

	// wg.Add and map write under the same lock to prevent a race with
	// Stop's drain-then-wg.Wait sequence.
	h.connMu.Lock()
	h.wg.Add(1)
	h.conns[conn.ID()] = conn
	h.connMu.Unlock()

	slog.Info("websocket hub: connection registered",
		slog.String("conn_id", conn.ID()),
	)

	go func() {
		defer h.wg.Done()
		h.readLoop(conn)
		h.Unregister(conn.ID())
	}()

	return nil
}

// Unregister removes a connection from the Hub and closes it.
func (h *Hub) Unregister(connID string) {
	h.connMu.Lock()
	conn, ok := h.conns[connID]
	if ok {
		delete(h.conns, connID)
	}
	h.connMu.Unlock()

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

// Broadcast sends a text message to all connected clients.
func (h *Hub) Broadcast(ctx context.Context, data []byte) {
	h.connMu.RLock()
	conns := make([]Conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.connMu.RUnlock()

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
	h.connMu.RLock()
	conn, ok := h.conns[connID]
	h.connMu.RUnlock()

	if !ok {
		return errcode.New(ErrWSConnNotFound,
			"websocket: connection not found: "+connID)
	}

	return conn.Write(ctx, data)
}

// Config returns the Hub's configuration.
func (h *Hub) Config() HubConfig { return h.config }

// ConnCount returns the number of active connections.
func (h *Hub) ConnCount() int {
	h.connMu.RLock()
	defer h.connMu.RUnlock()
	return len(h.conns)
}

// readLoop reads messages until conn.Close() breaks the Read call.
// No context is used — the loop exits solely on I/O error from Close.
func (h *Hub) readLoop(conn Conn) {
	for {
		data, err := conn.Read(context.Background())
		if err != nil {
			slog.Debug("websocket hub: read loop ended",
				slog.String("conn_id", conn.ID()),
				slog.Any("error", err),
			)
			return
		}
		if h.handler != nil {
			h.handler(context.Background(), conn.ID(), data)
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
	h.connMu.RLock()
	conns := make(map[string]Conn, len(h.conns))
	for k, v := range h.conns {
		conns[k] = v
	}
	h.connMu.RUnlock()

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
