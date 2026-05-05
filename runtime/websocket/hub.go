package websocket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/logutil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time assertion: Hub implements lifecycle.ManagedResource.
var _ kernellifecycle.ManagedResource = (*Hub)(nil)

// errHubNotRunning is pre-allocated to avoid per-call allocation in Checkers.
// Pattern: adapters/rabbitmq/connection.go:51 errHealthReconnecting.
var errHubNotRunning = errcode.New(errcode.KindUnavailable, errcode.ErrWSHubNotRunning,
	"websocket: hub is not running")

// Hub lifecycle states (atomic.Int32 transitions).
const (
	stateIdle     int32 = 0 // NewHub: no goroutines, ready to Start.
	stateRunning  int32 = 1 // Start called: ping loop active, Register allowed.
	stateStopping int32 = 2 // shutdown in progress: draining connections.
	stateStopped  int32 = 3 // Terminal: hub cannot be restarted.
)

const (
	defaultPingInterval         = 30 * time.Second
	defaultPingTimeout          = 5 * time.Second
	defaultReadLimit            = 64 * 1024 // 64KB
	defaultPingMissMax          = 2
	defaultShutdownTimeout      = 10 * time.Second
	defaultSendBufferSize       = 32
	defaultConcurrentCloseLimit = 64

	internalConnIDFmt = "conn_id=%s"
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
	//
	// SECURITY: Production deployments MUST set an explicit cap to prevent
	// goroutine exhaustion (each connection runs 2 goroutines: readLoop +
	// writeLoop). Recommended starting value: expected concurrent sessions
	// + 20% headroom. Token-authenticated unlimited capacity is a DoS path.
	MaxConnections int
	// Clock is the time source. Required; NewHub panics if nil.
	Clock clock.Clock

	// SendBufferSize is the per-connection send channel capacity used by the
	// writeLoop. When the channel is full the connection is evicted (slow
	// client; gorilla/websocket select-default-drop). Default 32; zero value
	// is replaced with the default at construction time. Must be > 0 if
	// explicitly set.
	SendBufferSize int

	// ShutdownTimeout limits the total time allowed for the external-cancel
	// shutdown path inside Start (caller's ctx is canceled with no deadline).
	// Stop(ctx) paths are not affected — caller's ctx governs directly.
	// 0 → defaultShutdownTimeout (10s).
	//
	// To bound Stop(ctx), supply a deadline on the ctx passed to Stop, or
	// configure bootstrap.WithShutdownTimeout for bootstrap-managed shutdown
	// — ShutdownTimeout only governs the external-cancel path inside Start.
	ShutdownTimeout time.Duration

	// ConcurrentCloseLimit bounds the semaphore pool used during shutdown drain
	// — at most this many conn.Close() calls run concurrently. Tuning guidance:
	// keep below system fd-limit / goroutine budget. Default 64 (matches
	// centrifuge's hubShutdownSemaphoreSize). Background goroutines spawned by
	// shutdown's outer wg.Wait/done-close pattern exit when readLoops/writeLoops
	// drain via Phase 1 context cancellation; max lifetime = connection teardown
	// latency.
	//
	// ref: centrifugal/centrifuge hub.go — bounded concurrent close
	ConcurrentCloseLimit int
}

// DefaultHubConfig returns a HubConfig with sensible defaults. A clock must be
// provided; pass clock.Real() at the composition root or a clockmock for tests.
func DefaultHubConfig(clk clock.Clock) HubConfig {
	return HubConfig{
		PingInterval:         defaultPingInterval,
		PingTimeout:          defaultPingTimeout,
		ReadLimit:            defaultReadLimit,
		PingMissMax:          defaultPingMissMax,
		SendBufferSize:       defaultSendBufferSize,
		ShutdownTimeout:      defaultShutdownTimeout,
		ConcurrentCloseLimit: defaultConcurrentCloseLimit,
		Clock:                clk,
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

// MustValidateHubConfig panics if cfg contains values that would cause
// runtime panics or zero-budget shutdown later. Called by NewHub. Exposed as
// a Must* function to satisfy archtest PANIC-REGISTERED-01 (panic() must
// live inside Must* functions or ADR-registered exceptions).
//
// Validates:
//   - ShutdownTimeout < 0 → would collapse external-cancel context to
//     already-expired
//   - ConcurrentCloseLimit < 0 → would panic at make(chan struct{}, limit)
//     during shutdown
//
// Zero values are accepted (NewHub falls back to package defaults).
func MustValidateHubConfig(cfg HubConfig) {
	if cfg.ShutdownTimeout < 0 {
		panic("websocket.NewHub: HubConfig.ShutdownTimeout must be >= 0")
	}
	if cfg.ConcurrentCloseLimit < 0 {
		panic("websocket.NewHub: HubConfig.ConcurrentCloseLimit must be >= 0")
	}
}

// NewHub creates a Hub. No background goroutines are started until Start.
//
// Fail-fast wiring: negative ShutdownTimeout / ConcurrentCloseLimit panic at
// construction (see MustValidateHubConfig). Zero values fall back to package
// defaults (10s / 64). This matches clock.MustHaveClock's panic-on-misconfig
// style — wiring bugs surface before any goroutine runs, never as a
// shutdown-time make-chan panic or an already-expired context.
func NewHub(cfg HubConfig, handler MessageHandler) *Hub {
	clock.MustHaveClock(cfg.Clock, "websocket.NewHub")
	MustValidateHubConfig(cfg)
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
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.ConcurrentCloseLimit == 0 {
		cfg.ConcurrentCloseLimit = defaultConcurrentCloseLimit
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

	slog.Info(
		"websocket hub: started",
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
	// Use the configured ShutdownTimeout (already defaulted to defaultShutdownTimeout
	// in NewHub) so tests can override the timeout without relying on hardcoded 10s.
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.WithoutCancel(ctx), h.config.ShutdownTimeout,
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

	// Close connections with bounded concurrency. Every entry is guaranteed
	// to have a Close goroutine spawned; the semaphore inside each goroutine
	// bounds concurrent transport teardown. ctx bounds *waiting* for
	// completion, never *whether* an entry's Close is scheduled — see
	// closeEntriesConcurrently for the contract.
	//
	// ref: centrifugal/centrifuge hub.go — semaphore-bounded Close fanout.
	// ref: nats-io/nats-server server.go closeAllClients — full-set scheduling
	// invariant; deadline bounds waiting only.
	closeEntriesConcurrently(ctx, entries, h.config.ConcurrentCloseLimit)

	// Wait for all goroutines (readLoops + writeLoops + pingLoop), bounded by ctx.
	// goroutine exits when readLoops/writeLoops drain via Phase 1 context
	// cancellation; max lifetime = connection teardown latency.
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
		slog.Error(
			"websocket hub: stop timed out, hub is poisoned",
			slog.Any("error", ctx.Err()),
		)
		err = ctx.Err()
	}

	h.state.Store(stateStopped)
	return err
}

// closeEntriesConcurrently cancels and closes each entry's connection using a
// semaphore-bounded goroutine pool of size limit.
//
// Contract under timeout: every entry MUST receive a transport-level Close()
// call. The ctx deadline bounds the *waiting* phase only — it does not
// truncate the scheduling phase. This is required because shutdown() has
// already cleared h.conns and h.subjectIdx by the time this function runs;
// readLoop's fallback (unregisterEntry) sees the entry as not-current and
// will not invoke Close, so a missed scheduling here leaks the transport
// (TCP) until OS-level FIN_WAIT.
//
// Phase 1 (always-runs): eagerly cancel + close-done for every entry so
// readLoop/writeLoop context-cancellation paths fire regardless of whether
// the per-entry conn.Close() ever runs.
//
// Phase 2 (always-launches): spawn one Close goroutine per entry. Each
// goroutine acquires a semaphore slot internally before invoking conn.Close,
// so the limit only bounds *concurrent* close work, never *whether* a Close
// is attempted. Total goroutine count is len(entries); each holds the
// goroutine stack (~2KB) until its slot is granted and Close completes.
//
// Final wait: ctx.Done() vs closeWG. On ctx expiry the function returns
// while the still-pending Close goroutines drain in background — their
// readLoops already exited via Phase 1 cancel, so no goroutine is leaked
// past connection-teardown latency.
//
// ref: centrifugal/centrifuge hub.go — semaphore-bounded Close fanout.
// ref: nats-io/nats-server server.go closeAllClients — every snapshot entry
// receives a close attempt; deadline bounds waiting, not scheduling.
func closeEntriesConcurrently(ctx context.Context, entries []*connEntry, limit int) {
	// Phase 1: eagerly cancel all entries (unblocks Read ctx-side + writeLoop).
	// This must happen before launching Close goroutines so context-driven
	// drain paths fire even if conn.Close() never runs.
	for _, e := range entries {
		e.cancel()
		e.closeOnce.Do(func() { close(e.done) })
	}

	// Phase 2: spawn one Close goroutine per entry; semaphore bounds in-flight
	// transport teardown work but does NOT gate scheduling. Every entry is
	// guaranteed to have a goroutine waiting to call its conn.Close().
	sem := make(chan struct{}, limit)
	var closeWG sync.WaitGroup
	for _, e := range entries {
		closeWG.Add(1)
		go func(e *connEntry) {
			defer closeWG.Done()
			sem <- struct{}{} // bounded wait for slot inside goroutine
			defer func() { <-sem }()
			if err := e.conn.Close(); err != nil {
				slog.Warn(
					"websocket hub: close connection failed",
					slog.String("conn_id", e.conn.ID()),
					slog.String("remote_addr", logutil.SafeAddr(e.conn.RemoteAddr())),
					slog.Any("error", err),
				)
			}
		}(e)
	}

	// Wait for all Close goroutines to drain, bounded by ctx. Goroutines exit
	// when readLoops/writeLoops drain via Phase 1 context cancellation; max
	// lifetime = connection teardown latency.
	allDone := make(chan struct{})
	go func() {
		closeWG.Wait()
		close(allDone)
	}()
	select {
	case <-allDone:
	case <-ctx.Done():
		// ctx expired; Close goroutines continue in background.
	}
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
		slog.Warn(
			"websocket hub: evicted duplicate conn",
			slog.String("conn_id", conn.ID()),
			slog.String("remote_addr", logutil.SafeAddr(evicted.conn.RemoteAddr())),
			slog.String("reason", "duplicate_conn_id"),
		)
	}

	slog.Info(
		"websocket hub: connection registered",
		slog.String("conn_id", conn.ID()),
		slog.String("remote_addr", logutil.SafeAddr(conn.RemoteAddr())),
		slog.String("subject", entry.subject),
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
			slog.Debug(
				"websocket hub: close on unregister",
				slog.String("conn_id", connID),
				slog.Any("error", err),
			)
		}
		slog.Info(
			"websocket hub: connection unregistered",
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
			slog.Debug(
				"websocket hub: close on unregister",
				slog.String("conn_id", connID),
				slog.Any("error", err),
			)
		}
		slog.Info(
			"websocket hub: connection unregistered",
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
			"websocket: BroadcastFilter requires a non-nil filter; use func(Conn) bool { return true } for full broadcast")
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
			"websocket: connection not found",
			errcode.WithInternal(fmt.Sprintf(internalConnIDFmt, connID)))
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case entry.send <- data:
		return nil
	case <-entry.done:
		return errcode.New(errcode.KindUnavailable, errcode.ErrWSConnNotFound,
			"websocket: connection already evicted",
			errcode.WithInternal(fmt.Sprintf(internalConnIDFmt, connID)))
	default:
		h.evictWith(entry, "send_buffer_full", slog.LevelWarn,
			"websocket hub: slow client evicted, send buffer full",
			"websocket hub: close on slow-client evict")
		return errcode.New(errcode.KindUnavailable, errcode.ErrWebsocketSlowClient,
			"websocket: connection send buffer full, evicted",
			errcode.WithInternal(fmt.Sprintf(internalConnIDFmt, connID)))
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

// ---------------------------------------------------------------------------
// lifecycle.ManagedResource implementation
// ---------------------------------------------------------------------------

// Checkers implements lifecycle.ManagedResource. It returns a single probe
// "websocket_hub_ready" that reports nil (healthy) only when the Hub is in
// the running state. Any other state (idle/stopping/stopped) returns
// errHubNotRunning.
//
// probe name "websocket_hub_ready" follows the observability rule:
// snake_case + "_ready" suffix.
//
// ref: adapters/rabbitmq/connection.go Checkers — probe name + pre-allocated error pattern.
func (h *Hub) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"websocket_hub_ready": func(_ context.Context) error {
			if h.state.Load() == stateRunning {
				return nil
			}
			return errHubNotRunning
		},
	}
}

// Compile-time assertion: hubWorker satisfies kernel/worker.Worker.
var _ worker.Worker = (*hubWorker)(nil)

// hubWorker adapts *Hub to the kernel/worker.Worker contract so that
// bootstrap.WithManagedResource(hub) auto-starts the hub via WorkerGroup —
// the same lifecycle-ownership model as uber-go/fx, go-kratos, and
// zeromicro/go-zero (managed runtime objects own start AND stop).
//
// Stop is idempotent: it swallows ErrWSAlreadyStopped because the same Hub
// is wired into two bootstrap teardown paths (WorkerGroup.Stop and
// ManagedResource.Close LIFO). Whichever fires first does the real shutdown;
// the second sees stateStopped and returns nil instead of an error.
type hubWorker struct{ h *Hub }

func (w *hubWorker) Start(ctx context.Context) error { return w.h.Start(ctx) }

func (w *hubWorker) Stop(ctx context.Context) error {
	err := w.h.Stop(ctx)
	if err == nil {
		return nil
	}
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == errcode.ErrWSAlreadyStopped {
		return nil
	}
	return err
}

// Worker returns a worker.Worker that drives Start/Stop so bootstrap can
// auto-manage the hub lifecycle.
//
// Composition root (PR #393, post-/fix): one of the two equivalent patterns
//
//	hub := websocket.NewHub(cfg, handler)
//	bootstrap.New(..., bootstrap.WithManagedResource(hub))
//	// hub.Start is invoked by the bootstrap WorkerGroup; Close runs LIFO
//	// during phase10. Do NOT also run `go hub.Start(ctx)` manually — the
//	// duplicate Start would return ErrWSAlreadyStarted.
//
// Manual mode (legacy / tests):
//
//	hub := websocket.NewHub(cfg, handler)
//	go func() { _ = hub.Start(ctx) }()
//	// caller is responsible for hub.Stop(ctx) on shutdown.
//
// ref: kernel/lifecycle/managed_resource.go::Worker — non-nil documents
// "auto-managed background worker"
// ref: uber-go/fx app.go Lifecycle — managed surface owns start+stop
// ref: go-kratos/kratos transport/transport.go Server — same contract
func (h *Hub) Worker() worker.Worker { return &hubWorker{h: h} }

// Close implements lifecycle.ManagedResource. It delegates to Stop(ctx) and
// is idempotent: if the Hub is already stopped, returns nil instead of the
// ErrWSAlreadyStopped error that Stop would return.
//
// Bootstrap wiring: bootstrap.WithManagedResource(hub) calls Close in LIFO
// order during phase10 shutdown.
//
// ref: adapters/rabbitmq/connection.go Close — idempotent + ctx-bounded.
func (h *Hub) Close(ctx context.Context) error {
	err := h.Stop(ctx)
	if err == nil {
		return nil
	}
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == errcode.ErrWSAlreadyStopped {
		slog.Debug(
			"websocket hub: Close called on already-stopped hub",
			slog.String("error", err.Error()),
		)
		return nil
	}
	return err
}

// readLoop reads messages until the per-conn context is canceled or
// conn.Close() breaks the Read call.
func (h *Hub) readLoop(ctx context.Context, conn Conn) {
	for {
		data, err := conn.Read(ctx)
		if err != nil {
			slog.Debug(
				"websocket hub: read loop ended",
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
				slog.Debug(
					"websocket hub: write failed in writeLoop",
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
		slog.Debug(
			closeMsg,
			slog.String("conn_id", connID),
			slog.String("reason", reason),
			slog.Any("error", err),
		)
	}
	slog.LogAttrs(
		context.Background(), level, evictMsg,
		slog.String("conn_id", connID),
		slog.String("remote_addr", logutil.SafeAddr(entry.conn.RemoteAddr())),
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
		h.connMu.Unlock()
		h.evictWith(current, "ping_threshold_exceeded", slog.LevelWarn,
			"websocket hub: ping threshold exceeded, removing connection",
			"websocket hub: close on ping-miss evict")
		return
	}
	h.connMu.Unlock()
	slog.Debug(
		"websocket hub: ping missed",
		slog.String("conn_id", connID),
		slog.Int("misses", current.pingMisses),
	)
}
