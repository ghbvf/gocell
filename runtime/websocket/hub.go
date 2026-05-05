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
	"github.com/ghbvf/gocell/runtime/auth"
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
	// client; gorilla/websocket select-default-drop). Default 32; zero value
	// is replaced with the default at construction time. Must be > 0 if
	// explicitly set.
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

// connEntry wraps a Conn with its per-connection context, ping state, send
// channel, and a snapshot of the principal fields captured at Register time.
//
// ref: centrifugal/centrifuge client.go — c.user / c.exp value snapshot at
// handshake; hub never re-reads conn.Principal() after registration.
type connEntry struct {
	conn       Conn
	cancel     context.CancelFunc
	pingMisses int
	send       chan []byte   // buffered, sized by HubConfig.SendBufferSize
	done       chan struct{} // closed when entry is evicted; writeLoop exit signal
	closeOnce  sync.Once     // guards close(done) for idempotent eviction

	// subject and expiresAt are frozen at Register time from conn.Principal().
	// Hub does not call conn.Principal() after registration — the principal is
	// treated as immutable for the lifetime of the connection.
	subject   string    // conn.Principal().Subject; empty for anonymous/service
	expiresAt time.Time // conn.Principal().ExpiresAt; zero = no expiry
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
	// Only populated for entries whose subject snapshot is non-empty.
	// Synced at: Register / unregisterEntry / Unregister / pingLoop expiry-evict
	// / slow-client evict / shutdown drain.
	// All writes to conns and subjectIdx MUST go through removeConnLocked.
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
	if cfg.SendBufferSize == 0 {
		cfg.SendBufferSize = defaultSendBufferSize
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
			return errcode.New(errcode.KindInternal, errcode.ErrWSAlreadyStarted, "websocket: hub already started")
		}
		return errcode.New(errcode.KindInternal, errcode.ErrWSAlreadyStopped, "websocket: hub already stopped")
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
		return errcode.New(errcode.KindInternal, errcode.ErrWSAlreadyStopped, "websocket: hub already stopped")
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
	// Bulk drain: clear both maps together. This is the only place that
	// bypasses removeConnLocked — bulk shutdown must clear both atomically.
	// All non-shutdown write paths must use removeConnLocked.
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
		e.closeOnce.Do(func() { close(e.done) })
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
//
// The per-connection context carries the connection's Principal (if present)
// so that MessageHandler can call auth.FromContext(ctx) for inbound ACL.
func (h *Hub) Register(ctx context.Context, conn Conn) error {
	h.connMu.Lock()
	s := h.state.Load()
	if s == stateStopping {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(errcode.KindUnavailable, errcode.ErrWSHubStopping, "websocket: hub is stopping, connection rejected")
	}
	if s != stateRunning {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(errcode.KindUnavailable, errcode.ErrWSHubNotRunning, "websocket: hub is not running, connection rejected")
	}

	if h.config.MaxConnections > 0 && len(h.conns) >= h.config.MaxConnections {
		h.connMu.Unlock()
		_ = conn.Close()
		return errcode.New(errcode.KindUnavailable, errcode.ErrWSMaxConns, "websocket: max connections reached")
	}

	// Evict existing entry with same ID to prevent context leak.
	var evicted *connEntry
	if old, ok := h.conns[conn.ID()]; ok {
		h.removeConnLocked(old)
		evicted = old
	}

	// Snapshot principal fields once at handshake time.
	// Hub never re-reads conn.Principal() after this point.
	// ref: centrifugal/centrifuge client.go — c.user / c.exp frozen at connect.
	var subject string
	var expiresAt time.Time
	if p := conn.Principal(); p != nil {
		subject = p.Subject
		expiresAt = p.ExpiresAt
	}

	connCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	// Inject principal into per-conn context so MessageHandler can do ACL
	// via auth.FromContext(ctx).
	if p := conn.Principal(); p != nil {
		connCtx = auth.WithPrincipal(connCtx, p)
	}

	entry := &connEntry{
		conn:      conn,
		cancel:    cancel,
		send:      make(chan []byte, h.config.SendBufferSize),
		done:      make(chan struct{}),
		subject:   subject,
		expiresAt: expiresAt,
	}
	h.conns[conn.ID()] = entry
	h.addToSubjectIdxLocked(entry)
	h.wg.Add(2) // readLoop + writeLoop
	h.connMu.Unlock()

	// Close evicted conn outside lock.
	if evicted != nil {
		evicted.cancel()
		evicted.closeOnce.Do(func() { close(evicted.done) })
		_ = evicted.conn.Close()
		slog.Warn("websocket hub: evicted duplicate conn",
			slog.String("conn_id", conn.ID()),
			slog.String("reason", "duplicate_conn_id"),
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

// removeConnLocked deletes entry from h.conns and h.subjectIdx atomically.
// Caller must hold connMu. This is the single allowed mutation site for
// h.conns outside of shutdown's bulk drain; SEC-FAIL-CLOSED-09 archtest
// forbids raw delete(h.conns,) / clear(h.conns) elsewhere in hub.go.
func (h *Hub) removeConnLocked(entry *connEntry) {
	delete(h.conns, entry.conn.ID())
	h.removeFromSubjectIdxLocked(entry)
}

// addToSubjectIdxLocked must be called with connMu held. Skips entries whose
// subject snapshot is empty (anonymous, service, missing principal).
func (h *Hub) addToSubjectIdxLocked(entry *connEntry) {
	if entry.subject == "" {
		return
	}
	bucket, ok := h.subjectIdx[entry.subject]
	if !ok {
		bucket = make(map[string]*connEntry)
		h.subjectIdx[entry.subject] = bucket
	}
	bucket[entry.conn.ID()] = entry
}

// removeFromSubjectIdxLocked must be called with connMu held.
func (h *Hub) removeFromSubjectIdxLocked(entry *connEntry) {
	if entry.subject == "" {
		return
	}
	bucket, ok := h.subjectIdx[entry.subject]
	if !ok {
		return
	}
	delete(bucket, entry.conn.ID())
	if len(bucket) == 0 {
		delete(h.subjectIdx, entry.subject)
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
		h.removeConnLocked(entry)
	} else {
		ok = false
	}
	h.connMu.Unlock()

	if ok {
		entry.cancel()
		// Signal writeLoop to exit via done channel (not by closing send).
		// ref: centrifugal/centrifuge internal/queue/queue.go — never close the
		// producer-facing channel to avoid send-on-closed-channel panics.
		entry.closeOnce.Do(func() { close(entry.done) })
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
		h.removeConnLocked(entry)
	}
	h.connMu.Unlock()

	if ok {
		entry.cancel()
		entry.closeOnce.Do(func() { close(entry.done) })
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
		return errcode.New(errcode.KindInternal, errcode.ErrWebsocketBroadcastFilterMissing,
			"websocket: BroadcastFilter requires a non-nil filter; "+
				"use func(Conn) bool { return true } for full broadcast")
	}

	// Snapshot entries under lock; run filter outside lock to avoid
	// deadlock if filter calls hub.Send (which also acquires connMu).
	// ref: olahol/melody melody.go — filter runs outside session lock.
	h.connMu.Lock()
	snapshot := make([]*connEntry, 0, len(h.conns))
	for _, e := range h.conns {
		snapshot = append(snapshot, e)
	}
	h.connMu.Unlock()

	selected := make([]*connEntry, 0, len(snapshot))
	for _, e := range snapshot {
		if filter(e.conn) {
			selected = append(selected, e)
		}
	}
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
		return errcode.New(errcode.KindInternal, errcode.ErrWebsocketBroadcastSubjectMissing,
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
// ctx cancellation causes fanout to return early without enqueuing remaining entries.
func (h *Hub) fanout(ctx context.Context, entries []*connEntry, data []byte) {
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case e.send <- data:
			// queued
		case <-e.done:
			// already evicted, skip silently
		default:
			h.evictWith(e, "send_buffer_full", slog.LevelWarn,
				"websocket hub: slow client evicted, send buffer full",
				"websocket hub: close on slow-client evict")
		}
	}
}

// Send sends a text message to a specific connection.
// If the connection's send buffer is full, the connection is evicted and
// ErrWebsocketSlowClient is returned. If ctx is already canceled, returns
// ctx.Err() without enqueuing.
func (h *Hub) Send(ctx context.Context, connID string, data []byte) error {
	// Pre-check: if ctx is already done, return immediately without touching
	// the send channel. This gives ctx.Done() priority over the send case
	// (Go select does not guarantee ordering when multiple cases are ready).
	if ctx.Err() != nil {
		return ctx.Err()
	}

	h.connMu.Lock()
	entry, ok := h.conns[connID]
	h.connMu.Unlock()

	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrWSConnNotFound,
			"websocket: connection not found: "+connID)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case entry.send <- data:
		return nil
	case <-entry.done:
		return errcode.New(errcode.KindUnavailable, errcode.ErrWSConnNotFound,
			"websocket: connection "+connID+" already evicted")
	default:
		h.evictWith(entry, "send_buffer_full", slog.LevelWarn,
			"websocket hub: slow client evicted, send buffer full",
			"websocket hub: close on slow-client evict")
		return errcode.New(errcode.KindUnavailable, errcode.ErrWebsocketSlowClient,
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
// Exits when entry.done is closed (connection evicted/removed) or the
// per-conn context is canceled. On write failure, evicts the connection.
//
// ref: centrifugal/centrifuge internal/queue/queue.go — select on done chan
// rather than closing the send channel to avoid send-on-closed-channel panics
// in concurrent producers (fanout, Send).
func (h *Hub) writeLoop(ctx context.Context, entry *connEntry) {
	for {
		select {
		case <-entry.done:
			return
		case <-ctx.Done():
			return
		case data := <-entry.send:
			if err := entry.conn.Write(ctx, data); err != nil {
				slog.Debug("websocket hub: write failed in writeLoop",
					slog.String("conn_id", entry.conn.ID()),
					slog.Any("error", err),
				)
				h.evictWith(entry, "connection_write_failed", slog.LevelWarn,
					"websocket hub: connection evicted on write failure",
					"websocket hub: close on write-failure evict")
				return
			}
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
			h.evictWith(entry, "token_expired", slog.LevelInfo,
				"websocket hub: connection evicted on token expiry",
				"websocket hub: close on expiry evict")
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, h.config.PingTimeout)
		err := entry.conn.Ping(pingCtx)
		cancel()
		h.handlePingResult(connID, err)
	}
}

// isExpired reports whether the entry's principal token has expired.
// Uses the expiresAt snapshot captured at Register time.
//
// Boundary semantics: expiresAt == now is treated as expired (RFC 7519 §4.1.4:
// "the current date/time MUST be before the expiration date/time listed in the
// exp claim"; on-or-after exp means rejection). Implementation uses
// !After(now) rather than Before(now) so the exact-tick case evicts.
func (h *Hub) isExpired(entry *connEntry, now time.Time) bool {
	if entry.expiresAt.IsZero() {
		return false
	}
	return !entry.expiresAt.After(now)
}

// evictWith removes an entry from conns + subjectIdx and tears down its
// goroutines. Idempotent: safe to call from any path (slow-client,
// token-expiry, write-failure, or future eviction reasons). reason is
// included in structured log fields for observability. evictMsg is logged at
// the supplied level on success; closeMsg is logged at Debug if conn.Close
// returns an error.
func (h *Hub) evictWith(entry *connEntry, reason string, level slog.Level, evictMsg, closeMsg string) {
	connID := entry.conn.ID()
	h.connMu.Lock()
	current, ok := h.conns[connID]
	if ok && current == entry {
		h.removeConnLocked(entry)
	} else {
		ok = false
	}
	h.connMu.Unlock()

	if !ok {
		return
	}
	entry.cancel()
	entry.closeOnce.Do(func() { close(entry.done) })
	if err := entry.conn.Close(); err != nil {
		slog.Debug(closeMsg,
			slog.String("conn_id", connID),
			slog.String("reason", reason),
			slog.Any("error", err),
		)
	}
	slog.LogAttrs(context.Background(), level, evictMsg,
		slog.String("conn_id", connID),
		slog.String("subject", entry.subject),
		slog.String("reason", reason),
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
		h.removeConnLocked(current)
		h.connMu.Unlock()
		slog.Warn("websocket hub: ping threshold exceeded, removing connection",
			slog.String("conn_id", connID),
			slog.Int("misses", current.pingMisses),
		)
		current.cancel()
		current.closeOnce.Do(func() { close(current.done) })
		_ = current.conn.Close()
		return
	}
	h.connMu.Unlock()
	slog.Debug("websocket hub: ping missed",
		slog.String("conn_id", connID),
		slog.Int("misses", current.pingMisses),
	)
}
