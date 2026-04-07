package websocket

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Hub lifecycle states (atomic.Int32 transitions).
const (
	stateIdle     int32 = 0 // NewHub: no goroutines, ready to Start.
	stateRunning  int32 = 1 // Start called: ping loop active, Register allowed.
	stateStopping int32 = 2 // Stop in progress: draining connections.
	stateStopped  int32 = 3 // Terminal: hub cannot be restarted.
)

const (
	defaultPingInterval = 30 * time.Second
	defaultPingTimeout  = 5 * time.Second
	defaultReadLimit    = 64 * 1024 // 64KB
	defaultPingMissMax  = 2
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
	// PingMissMax is the number of consecutive ping failures before a
	// connection is evicted. Default: 2.
	PingMissMax int
	// MaxConnections is the maximum number of concurrent connections.
	// 0 means unlimited. Default: 0.
	MaxConnections int
}

// DefaultHubConfig returns a HubConfig with sensible defaults.
func DefaultHubConfig() HubConfig {
	return HubConfig{
		PingInterval: defaultPingInterval,
		PingTimeout:  defaultPingTimeout,
		ReadLimit:    defaultReadLimit,
		PingMissMax:  defaultPingMissMax,
	}
}

// MessageHandler is called when a message is received from a client.
type MessageHandler func(ctx context.Context, connID string, data []byte)

// connEntry wraps a Conn with its per-connection context and ping state.
type connEntry struct {
	conn       Conn
	cancel     context.CancelFunc
	pingMisses int
}

// Hub manages WebSocket connections and provides signal-first broadcasting.
//
// Lifecycle: NewHub → Start (blocks) → Stop (terminal, single-use).
// A stopped Hub cannot be restarted; create a new one instead.
type Hub struct {
	config  HubConfig
	handler MessageHandler

	state atomic.Int32 // stateIdle → stateRunning → stateStopping → stateStopped

	// connMu guards conns map and serializes Register vs Stop's drain.
	// wg.Add MUST happen under connMu to prevent a race with Stop's wg.Wait.
	connMu sync.Mutex
	conns  map[string]*connEntry
	wg     sync.WaitGroup // tracks readLoop + pingLoop goroutines

	// cancelMu protects runCancel from concurrent Start/Stop access.
	cancelMu  sync.Mutex
	runCancel context.CancelFunc
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
	if cfg.PingMissMax == 0 {
		cfg.PingMissMax = defaultPingMissMax
	}
	if handler == nil {
		handler = func(context.Context, string, []byte) {}
	}

	return &Hub{
		config:  cfg,
		handler: handler,
		conns:   make(map[string]*connEntry),
	}
}

// Start begins the Hub's ping loop. It blocks until Stop is called or ctx
// is cancelled. Returns nil when stopped normally, ctx.Err() when the
// caller's context expires.
func (h *Hub) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)

	// CAS + runCancel + wg.Add under cancelMu so that Stop (which reads
	// runCancel under cancelMu) is guaranteed to see them all.
	h.cancelMu.Lock()
	if !h.state.CompareAndSwap(stateIdle, stateRunning) {
		h.cancelMu.Unlock()
		cancel()
		s := h.state.Load()
		if s == stateRunning {
			return errcode.New(ErrWSAlreadyStarted, "websocket: hub already started")
		}
		return errcode.New(ErrWSAlreadyStopped, "websocket: hub already stopped")
	}
	h.runCancel = cancel
	h.wg.Add(1)
	h.cancelMu.Unlock()

	slog.Info("websocket hub: started",
		slog.Duration("ping_interval", h.config.PingInterval),
	)

	go func() {
		defer h.wg.Done()
		h.pingLoop(runCtx)
	}()

	// Block until Stop cancels runCtx or the caller's ctx expires.
	<-runCtx.Done()

	// Distinguish Stop (normal) from external cancellation.
	if h.state.Load() >= stateStopping {
		return nil
	}
	return ctx.Err()
}

// Stop gracefully shuts down the Hub. It rejects new connections, cancels
// the ping loop, closes all connections (breaking readLoops), and waits
// for goroutines to exit. The provided ctx bounds the wait time.
//
// After Stop the Hub is in a terminal state and cannot be restarted.
func (h *Hub) Stop(ctx context.Context) error {
	if !h.state.CompareAndSwap(stateRunning, stateStopping) {
		// Allow Stop on idle hub (stop-before-start): transition to terminal.
		if h.state.CompareAndSwap(stateIdle, stateStopped) {
			return nil
		}
		return errcode.New(ErrWSAlreadyStopped, "websocket: hub already stopped")
	}

	// Cancel run context: stops pingLoop, unblocks Start.
	h.cancelMu.Lock()
	cancel := h.runCancel
	h.cancelMu.Unlock()
	cancel()

	// Drain all connections under connMu. After this, any Register call
	// that acquires connMu will see state >= stateStopping and reject.
	h.connMu.Lock()
	entries := make([]*connEntry, 0, len(h.conns))
	for _, e := range h.conns {
		entries = append(entries, e)
	}
	clear(h.conns)
	h.connMu.Unlock()

	// Close all connections concurrently.
	var closeWg sync.WaitGroup
	for _, e := range entries {
		closeWg.Add(1)
		go func(e *connEntry) {
			defer closeWg.Done()
			e.cancel()
			if err := e.conn.Close(); err != nil {
				slog.Warn("websocket hub: close connection failed",
					slog.String("conn_id", e.conn.ID()),
					slog.Any("error", err),
				)
			}
		}(e)
	}
	closeWg.Wait()

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
		slog.Error("websocket hub: stop timed out, hub is poisoned")
		err = ctx.Err()
	}

	h.state.Store(stateStopped)
	return err
}

// Register adds a connection to the Hub and starts reading from it.
// The read loop uses a per-connection context that is cancelled when the
// connection is unregistered or the Hub stops.
func (h *Hub) Register(conn Conn) error {
	h.connMu.Lock()
	s := h.state.Load()
	if s == stateStopping {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(ErrWSHubStopping, "websocket: hub is stopping, connection rejected")
	}
	if s != stateRunning {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(ErrWSHubNotRunning, "websocket: hub is not running, connection rejected")
	}

	if h.config.MaxConnections > 0 && len(h.conns) >= h.config.MaxConnections {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(ErrWSMaxConns, "websocket: max connections reached")
	}

	// Evict existing entry with same ID to prevent context leak.
	var evicted *connEntry
	if old, ok := h.conns[conn.ID()]; ok {
		delete(h.conns, conn.ID())
		evicted = old
	}

	connCtx, cancel := context.WithCancel(context.Background())
	entry := &connEntry{conn: conn, cancel: cancel}
	h.conns[conn.ID()] = entry
	h.wg.Add(1)
	h.connMu.Unlock()

	// Close evicted conn outside lock.
	if evicted != nil {
		evicted.cancel()
		_ = evicted.conn.Close()
		slog.Warn("websocket hub: evicted duplicate conn",
			slog.String("conn_id", conn.ID()),
		)
	}

	slog.Info("websocket hub: connection registered",
		slog.String("conn_id", conn.ID()),
	)

	go func() {
		defer h.wg.Done()
		h.readLoop(connCtx, conn)
		h.unregisterConn(conn)
	}()

	return nil
}

// unregisterConn is called by the readLoop goroutine. It only removes the
// entry if the conn pointer matches, preventing an evicted readLoop from
// deleting a replacement entry with the same ID.
func (h *Hub) unregisterConn(conn Conn) {
	h.connMu.Lock()
	entry, ok := h.conns[conn.ID()]
	if ok && entry.conn == conn {
		delete(h.conns, conn.ID())
	} else {
		ok = false
	}
	h.connMu.Unlock()

	if ok {
		entry.cancel()
		if err := entry.conn.Close(); err != nil {
			slog.Debug("websocket hub: close on unregister",
				slog.String("conn_id", conn.ID()),
				slog.Any("error", err),
			)
		}
		slog.Info("websocket hub: connection unregistered",
			slog.String("conn_id", conn.ID()),
		)
	}
}

// Unregister removes a connection from the Hub by ID and closes it.
// This is a force-remove: it always deletes the entry regardless of which
// conn object is registered.
func (h *Hub) Unregister(connID string) {
	h.connMu.Lock()
	entry, ok := h.conns[connID]
	if ok {
		delete(h.conns, connID)
	}
	h.connMu.Unlock()

	if ok {
		entry.cancel()
		if err := entry.conn.Close(); err != nil {
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

// Broadcast sends a text message to all connected clients concurrently.
// A slow connection does not block delivery to other connections.
func (h *Hub) Broadcast(ctx context.Context, data []byte) {
	h.connMu.Lock()
	entries := make([]*connEntry, 0, len(h.conns))
	for _, e := range h.conns {
		entries = append(entries, e)
	}
	h.connMu.Unlock()

	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(e *connEntry) {
			defer wg.Done()
			if err := e.conn.Write(ctx, data); err != nil {
				slog.Warn("websocket hub: broadcast write failed",
					slog.String("conn_id", e.conn.ID()),
					slog.Any("error", err),
				)
			}
		}(e)
	}
	wg.Wait()
}

// Send sends a text message to a specific connection.
func (h *Hub) Send(ctx context.Context, connID string, data []byte) error {
	h.connMu.Lock()
	entry, ok := h.conns[connID]
	h.connMu.Unlock()

	if !ok {
		return errcode.New(ErrWSConnNotFound,
			"websocket: connection not found: "+connID)
	}

	return entry.conn.Write(ctx, data)
}

// Config returns the Hub's configuration.
func (h *Hub) Config() HubConfig { return h.config }

// ConnCount returns the number of active connections.
func (h *Hub) ConnCount() int {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	return len(h.conns)
}

// readLoop reads messages until the per-conn context is cancelled or
// conn.Close() breaks the Read call.
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
		h.handler(ctx, conn.ID(), data)
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
	h.connMu.Lock()
	snapshot := make(map[string]*connEntry, len(h.conns))
	for k, v := range h.conns {
		snapshot[k] = v
	}
	h.connMu.Unlock()

	for connID, entry := range snapshot {
		pingCtx, cancel := context.WithTimeout(ctx, h.config.PingTimeout)
		err := entry.conn.Ping(pingCtx)
		cancel()

		if err != nil {
			h.connMu.Lock()
			current, ok := h.conns[connID]
			if ok {
				current.pingMisses++
				if current.pingMisses >= h.config.PingMissMax {
					delete(h.conns, connID)
					h.connMu.Unlock()
					slog.Warn("websocket hub: ping threshold exceeded, removing connection",
						slog.String("conn_id", connID),
						slog.Int("misses", current.pingMisses),
					)
					current.cancel()
					_ = current.conn.Close()
				} else {
					h.connMu.Unlock()
					slog.Debug("websocket hub: ping missed",
						slog.String("conn_id", connID),
						slog.Int("misses", current.pingMisses),
					)
				}
			} else {
				h.connMu.Unlock()
			}
		} else {
			h.connMu.Lock()
			if current, ok := h.conns[connID]; ok {
				current.pingMisses = 0
			}
			h.connMu.Unlock()
		}
	}
}
