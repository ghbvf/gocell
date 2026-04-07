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
	stateStopping int32 = 2 // shutdown in progress: draining connections.
	stateStopped  int32 = 3 // Terminal: hub cannot be restarted.
)

const (
	defaultPingInterval    = 30 * time.Second
	defaultPingTimeout     = 5 * time.Second
	defaultReadLimit       = 64 * 1024 // 64KB
	defaultPingMissMax     = 2
	defaultShutdownTimeout = 10 * time.Second
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
//
// Both Stop(ctx) and external cancellation of Start(ctx) converge on the
// same internal shutdown path. There is exactly one code path that drains
// connections and transitions to the terminal state.
type Hub struct {
	config  HubConfig
	handler MessageHandler

	state atomic.Int32 // stateIdle → stateRunning → stateStopping → stateStopped

	// connMu guards conns map and serializes Register vs shutdown's drain.
	// wg.Add MUST happen under connMu to prevent a race with wg.Wait.
	connMu sync.Mutex
	conns  map[string]*connEntry
	wg     sync.WaitGroup // tracks readLoop + pingLoop goroutines

	// cancelMu protects runCancel from concurrent Start/Stop access.
	cancelMu  sync.Mutex
	runCancel context.CancelFunc

	// shutdownDone is closed when shutdown completes. Lets concurrent
	// Stop callers wait for an in-progress shutdown (started by Start's
	// cancel path or an earlier Stop call).
	shutdownDone chan struct{}
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
		config:       cfg,
		handler:      handler,
		conns:        make(map[string]*connEntry),
		shutdownDone: make(chan struct{}),
	}
}

// Start begins the Hub's ping loop. It blocks until Stop is called or ctx
// is cancelled.
//
// If the caller's context is cancelled, Start runs a full shutdown
// (drain + close) automatically. Stop may still be called afterwards but
// will return immediately since the Hub is already stopped.
func (h *Hub) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)

	// CAS + runCancel + wg.Add under cancelMu so that shutdown (which reads
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

	// Block until Stop's shutdown cancels runCtx, or the caller's ctx expires.
	<-runCtx.Done()

	if h.state.Load() >= stateStopping {
		// shutdown was triggered by Stop (or a concurrent Start-cancel).
		// Wait for it to finish before returning.
		<-h.shutdownDone
		return nil
	}

	// External cancellation: run the single shutdown path ourselves.
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(), defaultShutdownTimeout,
	)
	defer shutdownCancel()
	_ = h.shutdown(shutdownCtx)
	return ctx.Err()
}

// Stop gracefully shuts down the Hub. It rejects new connections, cancels
// the ping loop, closes all connections (breaking readLoops), and waits
// for goroutines to exit. The provided ctx bounds the entire operation.
//
// After Stop the Hub is in a terminal state and cannot be restarted.
func (h *Hub) Stop(ctx context.Context) error {
	// Fast path: stop an idle hub that was never started.
	if h.state.CompareAndSwap(stateIdle, stateStopped) {
		close(h.shutdownDone)
		return nil
	}
	return h.shutdown(ctx)
}

// shutdown is the single shutdown path. Both Stop and Start's external-cancel
// converge here. The CAS(running→stopping) inside ensures exactly one caller
// executes the drain; others wait on shutdownDone.
func (h *Hub) shutdown(ctx context.Context) error {
	if !h.state.CompareAndSwap(stateRunning, stateStopping) {
		s := h.state.Load()
		if s == stateStopping {
			// Another goroutine is shutting down. Wait for it.
			select {
			case <-h.shutdownDone:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// Already stopped.
		return errcode.New(ErrWSAlreadyStopped, "websocket: hub already stopped")
	}

	// We won the CAS — we own the shutdown.
	defer close(h.shutdownDone)

	// Cancel run context: stops pingLoop, unblocks Start if still blocking.
	h.cancelMu.Lock()
	cancel := h.runCancel
	h.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}

	// Drain all connections under connMu. After this, any Register call
	// that acquires connMu will see state >= stateStopping and reject.
	h.connMu.Lock()
	entries := make([]*connEntry, 0, len(h.conns))
	for _, e := range h.conns {
		entries = append(entries, e)
	}
	clear(h.conns)
	h.connMu.Unlock()

	// Cancel per-conn contexts (unblocks Read) then close transports.
	for _, e := range entries {
		e.cancel()
	}
	for _, e := range entries {
		go func(e *connEntry) {
			if err := e.conn.Close(); err != nil {
				slog.Warn("websocket hub: close connection failed",
					slog.String("conn_id", e.conn.ID()),
					slog.Any("error", err),
				)
			}
		}(e)
	}

	// Wait for all goroutines (readLoops + pingLoop), bounded by ctx.
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
// connection is unregistered or the Hub shuts down.
//
// The Hub must be in the running state (Start called). Register on an
// idle, stopping, or stopped Hub returns an error and closes the conn.
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

// IsRunning reports whether the Hub is in the running state and can
// accept new connections. Use this to guard HTTP upgrade handlers.
func (h *Hub) IsRunning() bool { return h.state.Load() == stateRunning }

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
