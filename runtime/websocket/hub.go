package websocket

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
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
	defaultSendBufferSize  = 32
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
	// Clock is the time source. Required; NewHub panics if nil.
	Clock clock.Clock

	// SendBufferSize is the per-connection send channel capacity used by the
	// writeLoop. When the channel is full the connection is evicted (slow
	// client; gorilla/websocket select-default-drop). Default 32; 0 means
	// unbuffered (any send that doesn't immediately match a receive triggers
	// eviction — extreme fail-closed mode for low-latency scenarios).
	SendBufferSize int
}

// DefaultHubConfig returns a HubConfig with sensible defaults. A clock must be
// provided; pass clock.Real() at the composition root or a clockmock for tests.
func DefaultHubConfig(clk clock.Clock) HubConfig {
	return HubConfig{
		PingInterval:   defaultPingInterval,
		PingTimeout:    defaultPingTimeout,
		ReadLimit:      defaultReadLimit,
		PingMissMax:    defaultPingMissMax,
		SendBufferSize: defaultSendBufferSize,
		Clock:          clk,
	}
}

// MessageHandler is called when a message is received from a client.
type MessageHandler func(ctx context.Context, connID string, data []byte)

// connEntry wraps a Conn with its per-connection context, ping state, and send channel.
type connEntry struct {
	conn          Conn
	cancel        context.CancelFunc
	pingMisses    int
	send          chan []byte    // buffered, sized by HubConfig.SendBufferSize
	closeSendOnce sync.Once     // guards close(send) for idempotent close
}

// Hub manages WebSocket connections and provides signal-first broadcasting.
//
// Lifecycle: NewHub → Start (blocks) → Stop (terminal, single-use).
// A stopped Hub cannot be restarted; create a new one instead.
//
// Both Stop(ctx) and external cancellation of Start(ctx) converge on the
// same internal shutdown path. There is exactly one code path that drains
// connections and transitions to the terminal state.
//
// ref: centrifugal/centrifuge hub.go — sharded hub, semaphore-bounded
//
//	concurrent close, snapshot-then-release drain pattern.
//
// ref: olahol/melody hub.go — pointer-keyed session map, channel-based
//
//	exit signal (we use atomic CAS instead).
//
// ref: coder/websocket chat example — CloseNow + defer pattern for
//
//	connection teardown; CloseRead for context-based lifecycle.
type Hub struct {
	config  HubConfig
	clk     clock.Clock
	handler MessageHandler

	state atomic.Int32 // stateIdle → stateRunning → stateStopping → stateStopped

	// connMu guards conns map and serializes Register vs shutdown's drain.
	// wg.Add MUST happen under connMu to prevent a race with wg.Wait.
	connMu sync.Mutex
	conns  map[string]*connEntry
	// subjectIdx[subject][connID] = entry. Maintained in lockstep with conns.
	// Only populated for entries whose Conn.Principal().Subject is non-empty.
	// Synced at: Register / unregisterEntry / Unregister / pingLoop expiry-evict
	// / slow-client evict / shutdown drain.
	subjectIdx map[string]map[string]*connEntry
	wg         sync.WaitGroup // tracks readLoop + writeLoop + pingLoop goroutines

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
	clock.MustHaveClock(cfg.Clock, "websocket.NewHub")
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
		clk:          cfg.Clock,
		handler:      handler,
		conns:        make(map[string]*connEntry),
		subjectIdx:   make(map[string]map[string]*connEntry),
		shutdownDone: make(chan struct{}),
	}
}

// Start begins the Hub's ping loop. It blocks until Stop is called or ctx
// is canceled.
//
// If the caller's context is canceled, Start runs a full shutdown
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
			return errcode.New(errcode.KindInternal, ErrWSAlreadyStarted, "websocket: hub already started")
		}
		return errcode.New(errcode.KindInternal, ErrWSAlreadyStopped, "websocket: hub already stopped")
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
		context.WithoutCancel(ctx), defaultShutdownTimeout,
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
		return errcode.New(errcode.KindInternal, ErrWSAlreadyStopped, "websocket: hub already stopped")
	}

	// We won the CAS — we own the shutdown.
	defer close(h.shutdownDone)

	// Cancel run context: stops pingLoop, unblocks Start if still blocking.
	h.cancelMu.Lock()
	cancel := h.runCancel
	h.runCancel = nil
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
	clear(h.subjectIdx)
	h.connMu.Unlock()

	// Close all connections synchronously. cancel() unblocks Read via
	// context; Close() tears down the transport (coder/websocket CloseNow is
	// lock-free, so this never blocks behind Write). Both are belt-and-
	// suspenders: cancel works for fakeConn, Close works for coder/websocket.
	//
	// Synchronous close ensures Stop returns only after all transport
	// resources (including coder/websocket's internal timeoutLoop) are cleaned up.
	// If connection counts reach thousands, replace with concurrent close
	// + closeWg (see WS-OPS-02 in tech-debt-registry).
	for _, e := range entries {
		e.cancel()
		e.closeSendOnce.Do(func() { close(e.send) })
		if err := e.conn.Close(); err != nil {
			slog.Warn("websocket hub: close connection failed",
				slog.String("conn_id", e.conn.ID()),
				slog.Any("error", err),
			)
		}
	}

	// Wait for all goroutines (readLoops + writeLoops + pingLoop), bounded by ctx.
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

// Register adds a connection to the Hub and starts reading from it. ctx is used
// as the parent for per-connection values; cancellation is controlled by the
// Hub so request-scope cancellation does not immediately tear down accepted
// WebSocket connections after the HTTP handler returns.
// The read loop uses a per-connection context that is canceled when the
// connection is unregistered or the Hub shuts down.
//
// The Hub must be in the running state (Start called). Register on an
// idle, stopping, or stopped Hub returns an error and closes the conn.
func (h *Hub) Register(ctx context.Context, conn Conn) error {
	h.connMu.Lock()
	s := h.state.Load()
	if s == stateStopping {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(errcode.KindUnavailable, ErrWSHubStopping, "websocket: hub is stopping, connection rejected")
	}
	if s != stateRunning {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(errcode.KindUnavailable, ErrWSHubNotRunning, "websocket: hub is not running, connection rejected")
	}

	if h.config.MaxConnections > 0 && len(h.conns) >= h.config.MaxConnections {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(errcode.KindUnavailable, ErrWSMaxConns, "websocket: max connections reached")
	}

	// Evict existing entry with same ID to prevent context leak.
	var evicted *connEntry
	if old, ok := h.conns[conn.ID()]; ok {
		delete(h.conns, conn.ID())
		h.removeFromSubjectIdxLocked(old)
		evicted = old
	}

	connCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	entry := &connEntry{
		conn:   conn,
		cancel: cancel,
		send:   make(chan []byte, h.config.SendBufferSize),
	}
	h.conns[conn.ID()] = entry
	h.addToSubjectIdxLocked(entry)
	h.wg.Add(2) // readLoop + writeLoop
	h.connMu.Unlock()

	// Close evicted conn outside lock.
	if evicted != nil {
		evicted.cancel()
		evicted.closeSendOnce.Do(func() { close(evicted.send) })
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
		defer cancel() // ensures cancel is called when the goroutine exits
		h.readLoop(connCtx, conn)
		h.unregisterEntry(entry)
	}()

	go func() {
		defer h.wg.Done()
		h.writeLoop(connCtx, entry)
	}()

	return nil
}

// addToSubjectIdxLocked must be called with connMu held. Skips entries whose
// principal Subject is empty (anonymous, service, missing principal).
func (h *Hub) addToSubjectIdxLocked(entry *connEntry) {
	p := entry.conn.Principal()
	if p == nil || p.Subject == "" {
		return
	}
	bucket, ok := h.subjectIdx[p.Subject]
	if !ok {
		bucket = make(map[string]*connEntry)
		h.subjectIdx[p.Subject] = bucket
	}
	bucket[entry.conn.ID()] = entry
}

// removeFromSubjectIdxLocked must be called with connMu held.
func (h *Hub) removeFromSubjectIdxLocked(entry *connEntry) {
	p := entry.conn.Principal()
	if p == nil || p.Subject == "" {
		return
	}
	bucket, ok := h.subjectIdx[p.Subject]
	if !ok {
		return
	}
	delete(bucket, entry.conn.ID())
	if len(bucket) == 0 {
		delete(h.subjectIdx, p.Subject)
	}
}

// unregisterEntry is called by the readLoop goroutine. It only removes the
// map entry if the current value is the same *connEntry pointer, preventing
// an evicted readLoop from deleting a replacement entry with the same ID.
// Using *connEntry pointer comparison (always safe) instead of Conn interface
// comparison (panics if concrete type is not comparable).
func (h *Hub) unregisterEntry(entry *connEntry) {
	connID := entry.conn.ID()
	h.connMu.Lock()
	current, ok := h.conns[connID]
	if ok && current == entry {
		delete(h.conns, connID)
		h.removeFromSubjectIdxLocked(entry)
	} else {
		ok = false
	}
	h.connMu.Unlock()

	if ok {
		entry.cancel()
		// close send AFTER state cleared; writeLoop exits.
		// unregisterEntry is called exactly once from readLoop's defer.
		entry.closeSendOnce.Do(func() { close(entry.send) })
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

// Unregister removes a connection from the Hub by ID and closes it.
// This is a force-remove: it always deletes the entry regardless of which
// conn object is registered.
func (h *Hub) Unregister(connID string) {
	h.connMu.Lock()
	entry, ok := h.conns[connID]
	if ok {
		delete(h.conns, connID)
		h.removeFromSubjectIdxLocked(entry)
	}
	h.connMu.Unlock()

	if ok {
		entry.cancel()
		entry.closeSendOnce.Do(func() { close(entry.send) })
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

// BroadcastFilter sends data to every connection for which filter returns true.
// filter must be non-nil; passing nil returns ErrWebsocketBroadcastFilterMissing
// (fail-closed: full-broadcast must be expressed as `func(Conn) bool { return true }`).
//
// filter is invoked under no lock; implementations must be O(1) (a closure that
// reads Conn.Principal() is fine; database queries are an anti-pattern). filter
// may be invoked from a single goroutine (the caller's goroutine).
//
// A connection whose send buffer is full is evicted (slow client; the broadcast
// continues to other connections). Returns nil on success even if some
// connections were evicted; the eviction is logged and counted via slog.
//
// ref: olahol/melody melody.go BroadcastFilter
func (h *Hub) BroadcastFilter(ctx context.Context, data []byte, filter func(Conn) bool) error {
	if filter == nil {
		return errcode.New(errcode.KindInternal, ErrWebsocketBroadcastFilterMissing,
			"websocket: BroadcastFilter requires a non-nil filter; "+
				"use func(Conn) bool { return true } for full broadcast")
	}
	h.connMu.Lock()
	selected := make([]*connEntry, 0, len(h.conns))
	for _, e := range h.conns {
		if filter(e.conn) {
			selected = append(selected, e)
		}
	}
	h.connMu.Unlock()

	h.fanout(ctx, selected, data)
	return nil
}

// BroadcastToSubject sends data to every connection whose Principal.Subject
// matches the supplied subject. Subject "" returns ErrWebsocketBroadcastSubjectMissing
// (fail-closed: callers must declare an explicit identity). An unknown subject
// (no matching connections) is a no-op and returns nil.
//
// O(1) lookup via the hub's subject index (centrifuge-style). Connection
// eviction on full send buffer is the same as BroadcastFilter.
//
// ref: centrifugal/centrifuge hub.go connShard.users
func (h *Hub) BroadcastToSubject(ctx context.Context, subject string, data []byte) error {
	if subject == "" {
		return errcode.New(errcode.KindInternal, ErrWebsocketBroadcastSubjectMissing,
			"websocket: BroadcastToSubject requires a non-empty subject")
	}
	h.connMu.Lock()
	bucket := h.subjectIdx[subject]
	selected := make([]*connEntry, 0, len(bucket))
	for _, e := range bucket {
		selected = append(selected, e)
	}
	h.connMu.Unlock()

	h.fanout(ctx, selected, data)
	return nil
}

// fanout enqueues data on each entry's send chan; full-buffer triggers eviction.
// Caller must NOT hold connMu (fanout acquires it for evictions).
func (h *Hub) fanout(ctx context.Context, entries []*connEntry, data []byte) {
	for _, e := range entries {
		select {
		case e.send <- data:
			// queued
		default:
			h.evictSlow(e)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// evictSlow removes a slow client whose send buffer is full. Idempotent;
// safe to call from fanout, writeLoop, or pingLoop.
func (h *Hub) evictSlow(e *connEntry) {
	connID := e.conn.ID()
	h.connMu.Lock()
	current, ok := h.conns[connID]
	if ok && current == e {
		delete(h.conns, connID)
		h.removeFromSubjectIdxLocked(e)
	} else {
		ok = false
	}
	h.connMu.Unlock()

	if !ok {
		return
	}
	e.cancel()
	e.closeSendOnce.Do(func() { close(e.send) })
	if err := e.conn.Close(); err != nil {
		slog.Debug("websocket hub: close on slow-client evict",
			slog.String("conn_id", connID),
			slog.Any("error", err),
		)
	}
	var subject string
	if p := e.conn.Principal(); p != nil {
		subject = p.Subject
	}
	slog.Warn("websocket hub: slow client evicted, send buffer full",
		slog.String("conn_id", connID),
		slog.String("subject", subject),
	)
}

// Send sends a text message to a specific connection.
// If the connection's send buffer is full, the connection is evicted and
// ErrWebsocketSlowClient is returned.
func (h *Hub) Send(ctx context.Context, connID string, data []byte) error {
	h.connMu.Lock()
	entry, ok := h.conns[connID]
	h.connMu.Unlock()

	if !ok {
		return errcode.New(errcode.KindNotFound, ErrWSConnNotFound,
			"websocket: connection not found: "+connID)
	}

	select {
	case entry.send <- data:
		return nil
	default:
		h.evictSlow(entry)
		return errcode.New(errcode.KindUnavailable, ErrWebsocketSlowClient,
			"websocket: connection "+connID+" send buffer full, evicted")
	}
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

// readLoop reads messages until the per-conn context is canceled or
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

// writeLoop drains the entry's send channel and writes each message to conn.
// Exits when the send channel is closed (connection removed) or context is
// canceled. On write failure, evicts the connection.
//
// ref: gorilla/websocket examples/chat/hub.go — select-default-drop pattern
func (h *Hub) writeLoop(ctx context.Context, entry *connEntry) {
	for {
		select {
		case data, ok := <-entry.send:
			if !ok {
				return // chan closed → exit
			}
			if err := entry.conn.Write(ctx, data); err != nil {
				slog.Debug("websocket hub: write failed in writeLoop",
					slog.String("conn_id", entry.conn.ID()),
					slog.Any("error", err),
				)
				// Write fail also evicts (network error, conn dead)
				h.evictSlow(entry)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) pingLoop(ctx context.Context) {
	ticker := h.clk.NewTicker(h.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			h.pingAll(ctx)
		}
	}
}

func (h *Hub) pingAll(ctx context.Context) {
	h.connMu.Lock()
	snapshot := make(map[string]*connEntry, len(h.conns))
	maps.Copy(snapshot, h.conns)
	h.connMu.Unlock()

	now := h.clk.Now()
	for connID, entry := range snapshot {
		// Token expiry check first (before paying the network round-trip).
		if h.isExpired(entry, now) {
			h.evictExpired(entry)
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, h.config.PingTimeout)
		err := entry.conn.Ping(pingCtx)
		cancel()
		h.handlePingResult(connID, err)
	}
}

// isExpired reports whether the entry's principal token has expired.
func (h *Hub) isExpired(entry *connEntry, now time.Time) bool {
	p := entry.conn.Principal()
	if p == nil {
		return false
	}
	if p.ExpiresAt.IsZero() {
		return false
	}
	return p.ExpiresAt.Before(now)
}

// evictExpired removes a connection whose principal token has expired.
func (h *Hub) evictExpired(entry *connEntry) {
	connID := entry.conn.ID()
	h.connMu.Lock()
	current, ok := h.conns[connID]
	if ok && current == entry {
		delete(h.conns, connID)
		h.removeFromSubjectIdxLocked(entry)
	} else {
		ok = false
	}
	h.connMu.Unlock()

	if !ok {
		return
	}
	entry.cancel()
	entry.closeSendOnce.Do(func() { close(entry.send) })
	if err := entry.conn.Close(); err != nil {
		slog.Debug("websocket hub: close on expiry evict",
			slog.String("conn_id", connID),
			slog.Any("error", err),
		)
	}
	var subject string
	if p := entry.conn.Principal(); p != nil {
		subject = p.Subject
	}
	slog.Info("websocket hub: connection evicted on token expiry",
		slog.String("conn_id", connID),
		slog.String("subject", subject),
	)
}

// handlePingResult records a successful ping or increments the miss counter.
// On miss-threshold breach it removes the connection and cancels its context.
func (h *Hub) handlePingResult(connID string, pingErr error) {
	if pingErr == nil {
		h.connMu.Lock()
		if current, ok := h.conns[connID]; ok {
			current.pingMisses = 0
		}
		h.connMu.Unlock()
		return
	}
	h.connMu.Lock()
	current, ok := h.conns[connID]
	if !ok {
		h.connMu.Unlock()
		return
	}
	current.pingMisses++
	if current.pingMisses >= h.config.PingMissMax {
		delete(h.conns, connID)
		h.removeFromSubjectIdxLocked(current)
		h.connMu.Unlock()
		slog.Warn("websocket hub: ping threshold exceeded, removing connection",
			slog.String("conn_id", connID),
			slog.Int("misses", current.pingMisses),
		)
		current.cancel()
		current.closeSendOnce.Do(func() { close(current.send) })
		_ = current.conn.Close()
		return
	}
	h.connMu.Unlock()
	slog.Debug("websocket hub: ping missed",
		slog.String("conn_id", connID),
		slog.Int("misses", current.pingMisses),
	)
}

