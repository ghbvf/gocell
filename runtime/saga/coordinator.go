package saga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/saga/executor"
	"github.com/ghbvf/gocell/runtime/saga/internal/sagalog"
)

// Compile-time interface checks.
var _ healthz.RepoProber = (*Coordinator)(nil)

// ProbeCoordinatorReady is the healthz ProbeName for the saga coordinator
// journal readiness probe. Cell-side registration (RegisterReadiness) is
// tracked in #978 (saga-as-cell migration follow-up); this PR lands the const
// declaration so the PROBENAME-SEALED-FUNNEL-01 archtest golden inventory
// can lock the declared form. Wiring is not performed here to avoid scope
// creep — the coordinator is not yet a first-class Cell.
const ProbeCoordinatorReady healthz.ProbeName = "saga_coordinator_ready"

// UnsafeModeLabel is the slog field value emitted at Start() to alert operators
// that this Coordinator runs without distributed leader election.
const UnsafeModeLabel = "unsafe_no_leader"

// unregisteredDefinitionLabel collapses a DefinitionID that is not in the
// registry into a bounded sentinel, so saga_drive_total / saga_leader_elect_skip_total
// definition_id cardinality stays bounded by the compile-time registered set
// (OpenTelemetry producer-side cardinality discipline; the metrics-provider cap
// is a tripwire, not the primary bound). Mirrors the "_runtime" cell sentinel.
const unregisteredDefinitionLabel = "_unregistered"

// ---------------------------------------------------------------------------
// Coordinator lifecycle state machine
// ---------------------------------------------------------------------------

type coordState int32

const (
	coordStopped  coordState = iota // zero value = stopped
	coordStarting                   // Start() entered, goroutines launching
	coordRunning                    // tick loop active
	coordStopping                   // Stop() called, waiting for goroutines
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

const (
	defaultCoordPollInterval   = 200 * time.Millisecond
	defaultCoordClaimBatchSize = 16
	defaultCoordLeaseDuration  = 30 * time.Second
)

// Config holds tunable parameters for the Coordinator engine.
//
// NewCoordinator initializes c.cfg = DefaultConfig() BEFORE running options,
// so callers that never invoke WithConfig get the documented defaults. If
// WithConfig is supplied, it replaces the whole struct (assigns c.cfg = cfg);
// there is no partial-merge and no per-field substitution. Validate() then
// runs once and rejects zero values for PollInterval / ClaimBatchSize /
// LeaseDuration / HeartbeatInterval — those four fields MUST be positive.
// To override only some fields, start from the defaults:
//
//	cfg := saga.DefaultConfig()
//	cfg.PollInterval = 500 * time.Millisecond
//	c, err := saga.NewCoordinator(..., saga.WithConfig(cfg))
//
// HeartbeatInterval + LeaseDuration flow into the internally-constructed
// Executor as the single source of truth — #1181 F5 deleted the WithExecutor
// option that previously allowed callers to inject an Executor with a
// different journal / lease; claim and heartbeat are now guaranteed
// same-source by construction.
type Config struct {
	// PollInterval is how often tickLoop calls ClaimPending. Default 200ms.
	PollInterval time.Duration
	// ClaimBatchSize is the maximum number of instances claimed per tick.
	// Default 16. It is also the per-tick drive-concurrency bound: tickOnce
	// drives every claimed instance that passes the leader gate in its own
	// goroutine, so peak concurrent driveOne (and concurrent external step IO)
	// per tick ≈ ClaimBatchSize. Lower it when steps open many external
	// connections — claim count and drive fan-out are intentionally the same
	// knob, because a claimed instance holds a journal lease that is only kept
	// alive by the Executor heartbeat that starts inside driveOne; claiming more
	// than are driven concurrently would let the excess leases go stale while
	// parked. Decoupling claim batch from drive concurrency requires a resident
	// worker pool (claim-on-free-slot) and is deferred (ADR §8 / #978).
	ClaimBatchSize int
	// LeaseDuration is how long a claimed lease is held. Default 30s.
	// Also used as the per-instance distlock TTL in leader-elect mode AND
	// forwarded to the internal Executor for heartbeat lease renewal.
	LeaseDuration time.Duration
	// HeartbeatInterval is how often the internal Executor's per-step
	// heartbeat goroutine renews the lease. Default executor.DefaultHeartbeatInterval
	// (= 10s = LeaseDuration/3). Must satisfy
	// HeartbeatInterval * executor.HeartbeatLeaseSafetyFactor < LeaseDuration
	// so at least one heartbeat lands before expiry.
	HeartbeatInterval time.Duration
}

// DefaultConfig returns a Config with documented defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:      defaultCoordPollInterval,
		ClaimBatchSize:    defaultCoordClaimBatchSize,
		LeaseDuration:     defaultCoordLeaseDuration,
		HeartbeatInterval: executor.DefaultHeartbeatInterval,
	}
}

// Validate returns nil iff all fields are positive and the heartbeat /
// lease ratio is safe.
func (c Config) Validate() error {
	if c.PollInterval <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga coordinator: Config.PollInterval must be positive")
	}
	if c.ClaimBatchSize <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga coordinator: Config.ClaimBatchSize must be positive")
	}
	if c.LeaseDuration <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga coordinator: Config.LeaseDuration must be positive")
	}
	if c.HeartbeatInterval <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga coordinator: Config.HeartbeatInterval must be positive")
	}
	if c.HeartbeatInterval*executor.HeartbeatLeaseSafetyFactor >= c.LeaseDuration {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga coordinator: Config.HeartbeatInterval*2 must be < LeaseDuration")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Coordinator
// ---------------------------------------------------------------------------

// Coordinator is the in-process saga engine. It drives pending saga instances
// forward: claiming a batch, replaying event history, executing one Step.Run
// body outside any database transaction, then atomically recording the outcome
// (Append + outbox Emit) inside a short RunInTx call.
//
// By default it runs in a single process with NO distributed leader election
// (unsafe mode); Start() emits slog.Warn(mode="unsafe_no_leader"). Pass
// WithLeaderElect(distlock.Locker) to make it multi-process safe: each claimed
// instance is driven only after winning a per-instance distributed lock, and
// Start() emits slog.Info(mode="leader_elect") instead. See leader_elect.go.
//
// # Single sanctioned journal holder
//
// Coordinator is the only struct in this package that holds a
// journal.JournalCore field — the Heartbeat-free core of journal.Journal. The
// narrow type makes a centralized heartbeat loop a compile error
// (c.journal.Heartbeat is undefined); per-step lease renewal is funneled through
// the internal executor. Locked by SAGA-JOURNAL-HOLDER-SEAL-01 (only Coordinator
// may hold JournalCore; no struct may persist the Heartbeat-bearing full Journal)
// and SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 (#1209).
type Coordinator struct {
	// required deps — set by NewCoordinator, validated non-nil before opts loop
	journal    journal.JournalCore
	txRunner   persistence.TxRunner
	outboxEmit koutbox.Emitter
	registry   ksaga.Resolver

	// optional with defaults
	dispatcher           Dispatcher   // default NoopDispatcher{}
	logger               *slog.Logger // default slog.Default()
	cfg                  Config
	clock                clock.Clock
	tracer               wrapper.Tracer // default wrapper.NoopTracer{}
	observerCallDeadline time.Duration  // default executor.DefaultObserverCallDeadline

	// optional leader election (PR-05). nil locker → single-process unsafe
	// mode. leaderElectNil records a nil locker passed to WithLeaderElect so
	// NewCoordinator can fail-fast (strong-dependency wiring option). Held here
	// (NOT a journal.JournalCore / journal.Journal field) so
	// SAGA-JOURNAL-HOLDER-SEAL-01 is unaffected.
	locker         distlock.Locker
	leaderElectNil bool

	// executor is the per-step execution engine constructed internally by
	// NewCoordinator using c.journal + cfg.HeartbeatInterval / LeaseDuration.
	// #1181 F5: previously injected via WithExecutor — that path allowed a
	// caller to inject an Executor with a different journal / lease config,
	// breaking the "claim and heartbeat are same-source" invariant. The
	// invariant is now enforced by construction: NewCoordinator passes the full
	// journal.Journal value it receives to executor.NewExecutor (it satisfies
	// executor.Heartbeater), then stores only the JournalCore facet in c.journal.
	// observer / tracer are caller-configurable via WithObserver / WithTracer and
	// forwarded into the internal Executor.
	executor *executor.Executor
	observer executor.Observer // default NopObserver{}, fan-out via WithObserver

	// lifecycle (mirrors runtime/outbox.Relay)
	state   atomic.Int32
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{}
	wg      sync.WaitGroup

	// inflightLocks maps instanceID (idutil.SafeID) → inflightDrive for
	// instances currently being driven by driveOne. Stop walks it to release
	// in-flight distlocks on shutdown and to drain until empty before canceling
	// goroutines.
	inflightLocks sync.Map
}

// inflightDrive is the inflightLocks value: everything Stop needs about an
// instance currently being driven.
//
// release frees the per-instance distlock immediately via Driver.Release I/O
// (no-op in single-process mode). Used by the normal per-tick completion path:
// work done → immediate release is optimal.
//
// orphan stops lease renewal without a shutdown-time release round-trip; the
// distlock key expires on its lease TTL (~1×TTL from the last successful
// renewal, best-effort — see distlock.Lock.Orphan) so a competitor can take
// over (no-op in single-process mode). Used by Stop/shutdown so the release RPC
// cannot hang on
// an unreachable backend during process teardown. A still-running wedged step
// keeps its lock until TTL while the journal lease_id CAS continues to fence
// its late commits.
//
// Both are idempotent and mutually exclusive via distlock's shared sync.Once:
// a tickOnce release() after a Stop orphan() is a harmless no-op.
type inflightDrive struct {
	release      func()
	orphan       func()
	definitionID idutil.SafeID // for per-instance shutdown log fan-out (F4)
	leaseID      idutil.SafeID // for per-instance shutdown log fan-out (F4)
}

// NewCoordinator validates required deps and applies opts. Nil required deps
// return errcode.KindInvalid + ErrValidationFailed. A nil or typed-nil clock
// panics via clock.MustHaveClock (programmer error).
func NewCoordinator(
	j journal.Journal,
	tx persistence.TxRunner,
	em koutbox.Emitter,
	reg ksaga.Resolver,
	clk clock.Clock,
	opts ...Option,
) (*Coordinator, error) {
	if validation.IsNilInterface(j) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: journal required")
	}
	if validation.IsNilInterface(tx) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: txRunner required")
	}
	if validation.IsNilInterface(em) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: outboxEmit required")
	}
	if validation.IsNilInterface(reg) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: registry required")
	}
	clock.MustHaveClock(clk, "runtime/saga.NewCoordinator")

	c := &Coordinator{
		journal:              j,
		txRunner:             tx,
		outboxEmit:           em,
		registry:             reg,
		clock:                clk,
		dispatcher:           NoopDispatcher{},
		logger:               slog.Default(),
		cfg:                  DefaultConfig(),
		tracer:               wrapper.NoopTracer{},
		observer:             executor.NopObserver{},
		observerCallDeadline: executor.DefaultObserverCallDeadline,
		readyCh:              make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	if c.leaderElectNil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: WithLeaderElect locker must not be nil; pass a non-nil distlock.Locker or omit the option")
	}
	if err := c.cfg.Validate(); err != nil {
		return nil, err
	}
	// Construct the internal Executor with the full journal.Journal value j as
	// its Heartbeater so claim and heartbeat are guaranteed same-source by
	// type-system construction (#1181 F5). j (the parameter) carries Heartbeat;
	// c.journal stores only the JournalCore facet — see the struct doc. observer
	// / tracer flow from Coordinator-level options into Executor — single tracing
	// root, single observer fan-out.
	exec, err := executor.NewExecutor(
		j, clk,
		executor.WithHeartbeatInterval(c.cfg.HeartbeatInterval),
		executor.WithLeaseDuration(c.cfg.LeaseDuration),
		executor.WithObserver(c.observer),
		executor.WithTracer(c.tracer),
		executor.WithLogger(c.logger),
	)
	if err != nil {
		return nil, fmt.Errorf("runtime/saga: construct internal executor: %w", err)
	}
	c.executor = exec
	// Leader-elect uses LeaseDuration as the per-instance distlock TTL, which
	// distlock.Acquire rejects below distlock.MinTTL (sub-ms TTLs truncate to a
	// permanent lock in Redis). Fail fast at construction rather than silently
	// skipping every drive at runtime. Single-process mode (locker == nil) does
	// not use distlock, so a sub-ms lease is permitted there.
	if c.locker != nil && c.cfg.LeaseDuration < distlock.MinTTL {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: leader-elect LeaseDuration must be ≥ distlock.MinTTL; the lease doubles as the distlock TTL",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("LeaseDuration=%s minTTL=%s", c.cfg.LeaseDuration, distlock.MinTTL))))
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Lifecycle — mirrors runtime/outbox/relay.go Start/Stop/Ready
// ---------------------------------------------------------------------------

// Start launches tickLoop and blocks until ctx is canceled or Stop is called.
func (c *Coordinator) Start(ctx context.Context) error {
	if !c.state.CompareAndSwap(int32(coordStopped), int32(coordStarting)) {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"saga coordinator: already started or starting")
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	c.mu.Lock()
	c.cancel = cancel
	c.done = done
	c.wg.Add(1) // tickLoop only
	c.mu.Unlock()

	c.state.Store(int32(coordRunning))
	close(c.readyCh)

	if c.locker == nil {
		c.logger.WarnContext(ctx, "saga coordinator: started in UNSAFE single-process mode (no leader-elect)",
			slog.String("mode", UnsafeModeLabel))
	} else {
		c.logger.InfoContext(ctx, "saga coordinator: started with distlock leader election",
			slog.String("mode", LeaderElectModeLabel),
			slog.Duration("lease_ttl", c.cfg.LeaseDuration))
	}

	defer func() {
		c.wg.Wait()

		c.mu.Lock()
		c.cancel = nil
		c.done = nil
		c.readyCh = make(chan struct{}) // fresh open channel; next Start() will close it
		c.state.Store(int32(coordStopped))
		close(done)
		c.mu.Unlock()
	}()

	go func() { defer c.wg.Done(); c.tickLoop(ctx) }()

	<-ctx.Done()
	return nil
}

// Stop signals shutdown, drains inflight driveOne goroutines, then cancels
// the loops and waits for them to exit. Idempotent.
//
// Drain budget = the passed-in ctx (same budget as the rest of Stop). While
// draining, tickOnce short-circuits to stop accepting new claims and the
// per-step Executor heartbeats keep leases valid so inflight steps don't lose
// their lease mid-flight. If a non-cooperative Step.Run ignores ctx.Done() and
// exceeds the drain budget:
//
//   - cancel() fires anyway and Stop returns (best-effort).
//   - The orphaned step goroutine continues until it returns naturally; the
//     Executor heartbeat goroutine has by then exited, so the journal lease expires.
//   - In leader-elect mode, Stop orphans every in-flight distlock
//     (orphanInflightLocks): renewal is stopped without a shutdown-time release
//     round-trip, so the distlock key expires on its lease TTL (~1×TTL from the
//     last successful renewal, best-effort) and a competitor coordinator can
//     take over — bounded-TTL handoff, I/O-free so it cannot hang
//     on an unreachable backend during shutdown. After Stop, per-instance distlock
//     keys linger in the backend for up to Config.LeaseDuration before expiring
//     (vs the previous immediate-release behavior); a coordinator restarting within
//     that window will skip those instances until the lease lapses — a liveness
//     cost, not a safety degradation (the journal lease_id CAS continues to fence
//     late commits). A still-running wedged step keeps its lock until TTL while
//     the journal lease_id CAS continues to fence its late commits (the PR-05
//     efficiency-lock model, leader_elect.go). Cooperative steps already released
//     via tickOnce; orphanInflightLocks idempotently covers the
//     drain-budget-exhausted case.
//   - This is the inherent limit of cooperative cancellation in Go: the step
//     goroutine itself cannot be killed. Step authors are responsible for
//     selecting on ctx.Done() inside blocking primitives — see ksaga.StepFunc
//     godoc.
func (c *Coordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	state := coordState(c.state.Load())
	notStarted := c.cancel == nil && state == coordStopped
	alreadyStopping := state == coordStopping
	ready := c.readyCh
	c.mu.Unlock()

	if notStarted || alreadyStopping {
		return nil
	}

	// Wait until Start has transitioned to running.
	select {
	case <-ready:
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errcode.ErrConflict,
			"saga coordinator stop: timed out waiting for start", ctx.Err())
	}

	c.state.Store(int32(coordStopping))

	// Drain active leases before canceling goroutines. Per-step Executor
	// heartbeats are still running during this phase so leases stay valid.
	// Non-cooperative steps (those that ignore ctx) will continue until they
	// naturally finish; once driveOne returns, tickOnce deletes the lease from
	// inflightLocks.
	drainTicker := c.clock.NewTicker(c.cfg.PollInterval)
drain:
	for {
		select {
		case <-ctx.Done():
			break drain // budget exhausted; fall through to cancel
		case <-drainTicker.C():
			count := 0
			c.inflightLocks.Range(func(_, _ any) bool { count++; return true })
			if count == 0 {
				break drain
			}
		}
	}
	drainTicker.Stop()

	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.mu.Unlock()

	// Orphan any in-flight distlocks BEFORE canceling so the shutdown I/O-free
	// guarantee holds: cancel() would wake a cooperative-but-unfinished step,
	// whose tickOnce could then race a release() (Driver.Release RPC) ahead of
	// orphan() — exactly the blocking/hangable I/O Stop is designed to avoid.
	// Orphaning first consumes the lock's shared sync.Once, so the later
	// tickOnce release() is a harmless no-op. Orphan stops renewal without I/O
	// so it cannot hang on an unreachable backend; future renewals stop and the
	// key expires by TTL expiry (see orphanInflightLocks for the bound). The
	// journal lease_id CAS fences the orphaned step at commit.
	c.orphanInflightLocks(ctx)
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}

	select {
	case <-done:
		c.logger.InfoContext(ctx, "saga coordinator: stopped")
		return nil
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errcode.ErrConflict,
			"saga coordinator stop: timed out", ctx.Err())
	}
}

// orphanInflightLocks stops the per-instance distlock renewal for every drive
// still in inflightLocks, without performing a Driver.Release RPC. Called from
// Stop after cancel so a non-cooperative step that outlives the drain budget
// cannot hold its lock until process death. orphan is idempotent and mutually
// exclusive with the owning tickOnce's later release() via distlock's shared
// sync.Once — the tickOnce release() after a Stop orphan() is a harmless no-op
// (a no-op in single-process mode as well). Entries are left for the owning
// goroutine to delete from the map.
//
// After Stop, per-instance distlock keys linger in the backend until their
// lease expires (vs the previous immediate-release behavior); a coordinator
// restarting within that window will skip those instances until the lease
// lapses. orphan() stops scheduling future renewals immediately and cancels
// any in-flight renewal best-effort, but cancellation is not atomic with the
// backend: a renewal whose write already reached the backend at orphan time
// may extend the lease one more TTL window from its commit. The takeover bound
// is therefore ~Config.LeaseDuration from the last successful renewal (≈one TTL
// window from Stop), not a hard cap measured from the Stop call. This is the
// deliberate cost of I/O-free shutdown (resilient even when the backend is
// unreachable at shutdown).
func (c *Coordinator) orphanInflightLocks(ctx context.Context) {
	var n int
	c.inflightLocks.Range(func(key, val any) bool {
		d, ok := val.(inflightDrive)
		if !ok {
			return true
		}
		d.orphan()
		n++
		instanceID, _ := key.(idutil.SafeID)
		c.logger.LogAttrs(ctx, slog.LevelDebug, "saga: orphaned in-flight distlock at shutdown",
			sagalog.InstanceFields(instanceID, d.leaseID,
				slog.String("definition_id", string(d.definitionID)))...)
		return true
	})
	if n > 0 {
		c.logger.InfoContext(ctx, "saga: orphaned in-flight distlocks at shutdown", slog.Int("count", n))
	}
}

// Ready returns a channel that is closed once Start transitions to running.
func (c *Coordinator) Ready() <-chan struct{} {
	c.mu.Lock()
	ch := c.readyCh
	c.mu.Unlock()
	return ch
}

// RepoReady implements healthz.RepoProber. The probe is binary (ready / not
// ready), driven by two layers in order:
//
//  1. Coordinator lifecycle — the probe MUST report not-ready while the
//     instance is stopped / starting / stopping. A coordinator that has not
//     reached coordRunning cannot drive new claims even if the journal is
//     fine; conversely a stopping coordinator is draining inflight work and
//     should be drained out of load balancers.
//  2. Journal storage — when running, delegate to journal.RepoReady so an
//     underlying PG/mem outage flips readiness off without needing a
//     separate probe name (failure domains differ from the pool-level
//     postgres_ready; see .claude/rules/gocell/observability.md §"Cell 级别
//     Repo Readiness Probe").
//
// The unsafe single-process mode (UnsafeModeLabel) is signaled at Start()
// via slog.Warn — it does NOT toggle readiness, because PR-03's contract is
// "unsafe but ready to drive a saga in a single process". PR-05 leader-elect
// is the path for multi-process safety; this probe stays binary.
//
// Cell-side registration (RegisterReadiness funnel) is the Coordinator cell
// holder's responsibility (PR-09).
func (c *Coordinator) RepoReady(ctx context.Context) error {
	switch coordState(c.state.Load()) {
	case coordStopped:
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"saga coordinator: not running")
	case coordStarting:
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"saga coordinator: starting")
	case coordStopping:
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"saga coordinator: stopping")
	}
	return c.journal.RepoReady(ctx)
}

// ---------------------------------------------------------------------------
// tickLoop
// ---------------------------------------------------------------------------

func (c *Coordinator) tickLoop(ctx context.Context) {
	ticker := c.clock.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if err := c.tickOnce(ctx); err != nil {
				c.logger.WarnContext(ctx, "saga: tick failed",
					slog.Int("batch_size", c.cfg.ClaimBatchSize),
					slog.Any("error", err))
			}
		}
	}
}

func (c *Coordinator) tickOnce(ctx context.Context) error {
	// Do not claim new instances while draining for Stop().
	if coordState(c.state.Load()) == coordStopping {
		return nil
	}
	claimed, _, err := c.journal.ClaimPending(ctx, c.cfg.ClaimBatchSize, c.cfg.LeaseDuration)
	if err != nil {
		c.safeObserve(ctx, "ObserveTick", func() { c.observer.ObserveTick(ctx, executor.TickError) })
		return fmt.Errorf("ClaimPending: %w", err)
	}
	if len(claimed) == 0 {
		c.safeObserve(ctx, "ObserveTick", func() { c.observer.ObserveTick(ctx, executor.TickEmpty) })
		return nil
	}
	c.safeObserve(ctx, "ObserveTick", func() { c.observer.ObserveTick(ctx, executor.TickClaimed) })
	// Concurrent fan-out (#983): drive each led instance in its own goroutine.
	// Peak concurrency = the number of led instances ≤ ClaimBatchSize — claim
	// count IS the concurrency bound (a claimed instance holds a journal lease
	// kept alive only by the Executor heartbeat that starts inside driveOne, so
	// every claimed instance must drive immediately rather than park; see the
	// Config.ClaimBatchSize godoc). The tick still waits for the whole batch
	// (wg.Wait) before returning, so the "one batch at a time" semantics and
	// Stop's inflight drain are unchanged — only per-instance driving within a
	// batch is parallelized. acquireLead + the leader gate stay serial in the
	// loop body so every drive passes the gate before any goroutine spawns;
	// inflightLocks.Store also stays serial (before go) so the entry is visible
	// the instant the drive could run (closes the drain gap).
	var wg sync.WaitGroup
	for _, ci := range claimed {
		// Leader-elect gate: in multi-process mode only the holder of the
		// per-instance distlock drives it; others skip this tick (no-lock →
		// skip). Single-process mode (no WithLeaderElect) always leads. This is
		// the sole driveOne call site, locked by SAGA-DRIVE-BEHIND-LEADER-GATE-01.
		release, orphan, lead := c.acquireLead(ctx, ci)
		if !lead {
			continue
		}
		// Use ci.LeaseID (per-instance fencing token) exclusively; the batch-level
		// leaseID from ClaimPending is discarded. PG Journal (PR-04) mints
		// per-instance tokens; using the batch token would break CAS fencing.
		c.inflightLocks.Store(ci.Instance.ID, inflightDrive{
			release:      release,
			orphan:       orphan,
			definitionID: ci.Instance.DefinitionID,
			leaseID:      ci.LeaseID,
		})
		wg.Add(1)
		go func(ci journal.ClaimedInstance, release func()) {
			// wg.Done is the outermost defer (runs last) so wg.Wait()/the Stop
			// drain never observe a half-cleaned entry: Delete+release run first.
			defer wg.Done()
			defer func() {
				c.inflightLocks.Delete(ci.Instance.ID)
				release()
			}()
			driveErr := c.driveOne(ctx, ci)
			c.observeDrive(ctx, ci, driveErr)
		}(ci, release)
	}
	wg.Wait()
	return nil
}

// observeDrive emits the per-drive result log + ObserveDrive metric for a
// completed driveOne. Extracted from the tickOnce goroutine so tickOnce stays
// within the cognitive-complexity limit; it MUST NOT call driveOne — the sole
// driveOne call site stays lexically in tickOnce per
// SAGA-DRIVE-BEHIND-LEADER-GATE-01 A1.
func (c *Coordinator) observeDrive(ctx context.Context, ci journal.ClaimedInstance, driveErr error) {
	driveResult := executor.DriveOK
	if driveErr != nil {
		driveResult = executor.DriveError
		// Sentinel-aware severity: ErrSagaStaleLease (handoff race) → Info;
		// ErrSagaNotFound (instance gone) → Warn; default → Warn. Keeps
		// multi-coordinator deployments from spamming WARN dashboards on every
		// lease lost during normal handoff.
		c.logger.LogAttrs(ctx, journalErrLevel(driveErr), "saga: drive failed",
			sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.Any("error", driveErr))...)
	}
	defID := ci.Instance.DefinitionID
	c.safeObserve(ctx, "ObserveDrive", func() { c.observer.ObserveDrive(ctx, c.labelDefinitionID(defID), driveResult) })
}

// safeObserve runs a Coordinator-emitted Observer call (ObserveTick /
// ObserveDrive / ObserveLeaderSkip) with two layers of fail-closed protection,
// symmetric with executor.(*Executor).callObserverBounded (executor/executor.go):
//
//  1. Panic recovery: defer recoverObserverPanic so a panicking observer logs
//     Warn with a redacted payload and execution continues.
//
//  2. Bounded wait: the observer call runs on a fresh goroutine; the caller
//     waits at most c.observerCallDeadline (default
//     executor.DefaultObserverCallDeadline = 5s) before logging Warn and
//     returning. This prevents a hung observer from leaking the per-instance
//     distlock (within each tickOnce drive goroutine, release() and
//     c.inflightLocks.Delete run as deferred cleanup AFTER observeDrive — and
//     thus the ObserveDrive call — returns) and from blocking the shutdown drain.
//
// The leaked observer goroutine may continue running indefinitely — bounded
// only by observer behavior, not by the coordinator (Go cannot kill a
// goroutine). Memory leaks are bounded by Observer impl quality; the Observer
// contract reminds implementers MUST NOT block.
func (c *Coordinator) safeObserve(ctx context.Context, method string, call func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer c.recoverObserverPanic(ctx, method)
		call()
	}()
	timer := c.clock.NewTimerAt(c.clock.Now().Add(c.observerCallDeadline))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C():
		c.logger.WarnContext(ctx, "saga coordinator: observer call exceeded deadline; continuing",
			slog.String("method", method),
			slog.Duration("deadline", c.observerCallDeadline),
		)
	}
}

// recoverObserverPanic is the shared recover handler for Coordinator-emitted
// observer calls. The panic payload is redacted through redaction.RedactAny
// before reaching slog so a panic value carrying user data does not leak into
// operator logs — same form as executor.recoverObserverPanic (executor.go).
func (c *Coordinator) recoverObserverPanic(ctx context.Context, method string) {
	if r := recover(); r != nil {
		c.logger.WarnContext(ctx, "saga coordinator: observer call panicked, ignoring",
			slog.String("method", method),
			slog.Any("panic", redaction.RedactAny(r)))
	}
}

// labelDefinitionID maps a DefinitionID to its metric-label form, collapsing
// any definition not in the registry to unregisteredDefinitionLabel.
//
// This bounds the definition_id Prometheus label cardinality to the
// compile-time registered set so an instance referencing a definition removed
// in a later deploy (or a corrupted/injected ID) cannot inject an arbitrary
// high-cardinality label value into saga_drive_total /
// saga_leader_elect_skip_total (OpenTelemetry producer-side cardinality
// discipline; the metrics-provider cap is a tripwire, not the primary bound).
func (c *Coordinator) labelDefinitionID(definitionID idutil.SafeID) string {
	if _, ok := c.registry.Lookup(definitionID); ok {
		return string(definitionID)
	}
	return unregisteredDefinitionLabel
}

// ---------------------------------------------------------------------------
// driveOne — core step execution
// ---------------------------------------------------------------------------

func (c *Coordinator) driveOne(ctx context.Context, ci journal.ClaimedInstance) (driveErr error) {
	// Top-level per-instance span. Child spans (saga.executor.step.run /
	// saga.executor.step.compensate) are owned by the Executor. Tracer
	// defaults to NoopTracer so this is zero-allocation in tests.
	ctx, span := c.tracer.Start(
		ctx, "saga.coordinator.driveOne",
		wrapper.Attr{Key: "saga.instance_id", Value: string(ci.Instance.ID)},
		wrapper.Attr{Key: "saga.definition_id", Value: string(ci.Instance.DefinitionID)},
		wrapper.Attr{Key: "saga.lease_id", Value: string(ci.LeaseID)},
	)
	defer func() {
		if driveErr != nil {
			// Redaction applied at sink (otelSpan.RecordError in
			// adapters/otel/span.go); pass raw error here.
			span.RecordError(driveErr)
			span.SetStatus(wrapper.StatusError, "driveOne returned err")
		}
		span.End()
	}()

	// 1. Lookup definition; missing → MarkTerminal Failed.
	def, ok := c.registry.Lookup(ci.Instance.DefinitionID)
	if !ok {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusFailed)
	}

	// 2. Total timeout check: elapsed > def.Timeout → MarkTerminal Expired.
	if def.Timeout > 0 && c.clock.Now().Sub(ci.Instance.StartedAt) > def.Timeout {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusExpired)
	}

	// 3. Replay events → compute cursor + prevState.
	events, err := c.journal.Load(ctx, ci.Instance.ID)
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}

	// 3a. Status-recovery branch (#1181 F1). A reclaimed instance whose
	// projection is Compensating (KindCompensationStarted already on the log)
	// must NOT run any forward Step.Run — that would produce duplicate
	// external side-effects on leader handoff or process restart mid-rollback.
	// runCompensation is idempotent: it derives the remaining work from
	// events (committed - already-compensated) and short-circuits when the
	// reverse walk is complete.
	if ci.Instance.Status == ksaga.StatusCompensating {
		return c.runCompensation(ctx, ci, def, events, nil /* trigger reason recovered from events */)
	}

	cursor, prevState, foldErr := foldEvents(events, def)
	if foldErr != nil {
		c.logger.LogAttrs(ctx, slog.LevelWarn, "saga: fold failed, marking terminal",
			sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.Any("error", foldErr))...)
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusFailed)
	}
	if cursor >= def.Len() {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusSucceeded)
	}

	// 4. Delegate to executor.Execute — retry / per-step timeout / heartbeat /
	// lease-loss all owned by the Executor. Coordinator applies saga-level
	// total deadline on the context before handing off.
	nextStep := def.Steps[cursor]
	runCtx := ctx
	if def.Timeout > 0 {
		totalDeadline := ci.Instance.StartedAt.Add(def.Timeout)
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(ctx, totalDeadline)
		defer cancel()
	}
	res := c.executor.Execute(runCtx, &ci.Instance, ci.LeaseID, nextStep, def.RetryPolicy, prevState)

	// 5. Route Outcome.
	return c.routeOutcome(ctx, ci, def, events, cursor, nextStep, res)
}

// routeOutcome maps an executor.Result outcome to the appropriate coordinator
// action. Extracted from driveOne to stay within the cognitive-complexity limit.
func (c *Coordinator) routeOutcome(
	ctx context.Context,
	ci journal.ClaimedInstance,
	def *ksaga.Definition,
	events []journal.Event,
	cursor int,
	nextStep ksaga.Step,
	res executor.Result,
) error {
	switch res.Outcome {
	case executor.OutcomeSucceeded:
		return c.commitStepInTx(ctx, commitStepArgs{
			instanceID: ci.Instance.ID,
			leaseID:    ci.LeaseID,
			defID:      def.ID,
			step:       nextStep,
			newState:   res.NewState,
			isLastStep: cursor == def.Len()-1,
		})
	case executor.OutcomeFailed:
		if shouldCompensate(events, def) {
			return c.runCompensation(ctx, ci, def, events, res.Err)
		}
		return c.commitStepInTx(ctx, commitStepArgs{
			instanceID: ci.Instance.ID,
			leaseID:    ci.LeaseID,
			defID:      def.ID,
			step:       nextStep,
			runErr:     res.Err,
			isLastStep: false, // commitStepFailed handles Terminal directly
		})
	case executor.OutcomeExpired:
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusExpired)
	case executor.OutcomeCanceled:
		// Parent ctx explicitly canceled — do not write terminal state;
		// let another coordinator re-claim on the next tick.
		return nil
	case executor.OutcomeLeaseLost:
		c.logger.LogAttrs(ctx, slog.LevelInfo, "saga: lease lost during step run; another leader took over",
			sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
				slog.String("definition_id", string(def.ID)))...)
		return nil
	default:
		return fmt.Errorf("saga: unknown executor outcome: %v", res.Outcome)
	}
}

// commitStepInTx wraps commitStep inside a RunInTx call and fires the
// AfterCommit dispatcher Kick hook.
func (c *Coordinator) commitStepInTx(ctx context.Context, args commitStepArgs) error {
	return c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := c.commitStep(txCtx, args); err != nil {
			return err
		}
		persistence.RegisterAfterCommit(txCtx, func(hookCtx context.Context) {
			c.dispatcher.Kick(hookCtx)
		})
		return nil
	})
}

// shouldCompensate returns true when the saga should enter the Compensating
// phase: at least one step in def has a non-nil Compensate function AND at
// least one KindStepCompleted event exists in the history (meaning committed
// work must be undone).
func shouldCompensate(events []journal.Event, def *ksaga.Definition) bool {
	// Check if any step has a compensate handler (short-circuit).
	hasCompensate := false
	for i := range def.Steps {
		if def.Steps[i].Compensate != nil {
			hasCompensate = true
			break
		}
	}
	if !hasCompensate {
		return false
	}
	// Check if any step has been committed.
	for i := range events {
		if events[i].Kind == journal.KindStepCompleted {
			return true
		}
	}
	return false
}

// runCompensation drives reverse compensation for a failed saga. It appends
// KindCompensationStarted, then reverse-walks committed steps calling
// Compensate on each. The entire walk runs under a single RunWithHeartbeat
// call to keep the lease alive throughout. Per-step compensate errors are
// accumulated (best-effort continue); final status is Compensated if all
// steps compensated cleanly; CompensationFailed if any step's Compensate
// fails and errors are accumulated without aborting the rollback walk.
// StatusFailed remains the terminal for forward-failure paths where
// compensation was never entered.
//
// ref: itimofeev/go-saga coordinator.go abort() — best-effort reverse
// compensation with error aggregation.
// runErr is the trigger error from the forward-step failure that originally
// invoked compensation. It is nil when this is a RECOVERY invocation
// (#1181 F1): driveOne re-entered runCompensation because the projection
// is already Compensating (a prior coordinator crashed mid-rollback or a
// leader handoff occurred between KindCompensationStarted and StatusCompensated).
// In recovery mode appendCompensationStarted is skipped (event already on
// log) and the reverse walk derives remaining work from events. Prior
// KindStepCompensationFailed events in the log are treated as known failures
// (not retried) and seed compensateErrors so recovery still terminates with
// StatusCompensationFailed rather than StatusCompensated.
func (c *Coordinator) runCompensation(
	ctx context.Context,
	ci journal.ClaimedInstance,
	def *ksaga.Definition,
	events []journal.Event,
	runErr error,
) error {
	// Step a: append KindCompensationStarted unless we are recovering — in
	// recovery the event is already on the log (otherwise the projected
	// Status would not be Compensating).
	recovering := ci.Instance.Status == ksaga.StatusCompensating
	if !recovering {
		if err := c.appendCompensationStarted(ctx, ci, runErr); err != nil {
			return err
		}
	}

	// Step b: collect committed steps + build step index. Steps that already
	// emitted KindStepCompensated / KindStepCompensationFailed are filtered
	// out so a recovering reverse walk only runs the work that remains
	// (#1181 F2). Side-effects are NOT re-applied on recovery.
	// priorFailureCount carries the count of KindStepCompensationFailed
	// events already in the log so that a recovery with no remaining work
	// still terminates as StatusCompensationFailed (#1181 F1).
	committed, stepByName, priorFailureCount := collectCommittedSteps(events, def)

	// Step c: reverse-walk under heartbeat.
	// Seed compensateErrors from historical failures so that a recovery
	// run where all remaining steps were already attempted (and failed)
	// does not silently produce StatusCompensated.
	// Each prior failure is represented by a sentinel error; the actual
	// error text was already persisted in the KindStepCompensationFailed
	// event payload — this is an idempotent "known failure, do not retry"
	// signal aligned with itimofeev/go-saga compensateErrors accumulation.
	compensateErrors := make([]error, 0, priorFailureCount)
	for i := 0; i < priorFailureCount; i++ {
		compensateErrors = append(compensateErrors, errors.New("prior compensation failure (not retried)"))
	}
	walkErr := c.executor.RunWithHeartbeat(ctx, &ci.Instance, ci.LeaseID, func(hbCtx context.Context) error {
		c.reverseWalkCompensate(hbCtx, ci, committed, stepByName, &compensateErrors)
		// hbCtx.Err() is propagated so RunWithHeartbeat's lease-loss override
		// kicks in when the cancel-cause is errLeaseLost; the outer
		// IsLeaseLost(walkErr) check then routes to the "another coordinator
		// took over" path. Returning bare nil would short-circuit that override.
		return hbCtx.Err()
	})

	// Step d: if lease was lost, return nil — another coordinator will take over.
	if executor.IsLeaseLost(walkErr) {
		return nil
	}
	if walkErr != nil {
		return fmt.Errorf("runCompensation: RunWithHeartbeat: %w", walkErr)
	}

	// Step e: determine final status.
	// Compensation step failures → StatusCompensationFailed (distinct from
	// StatusFailed which signals a forward-phase failure with no rollback).
	if len(compensateErrors) > 0 {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusCompensationFailed)
	}
	return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusCompensated)
}

// reverseWalkCompensate iterates committed steps in reverse order under an
// active heartbeat, invoking compensateOneStep for each. Errors are appended
// to compensateErrors (slice owned by the caller). The walk short-circuits on
// hbCtx.Err() so a stale-lease cancellation drops out without performing
// further side effects. Extracted from runCompensation to keep that
// function's cognitive complexity ≤ 15.
func (c *Coordinator) reverseWalkCompensate(
	hbCtx context.Context,
	ci journal.ClaimedInstance,
	committed []committedStepEntry,
	stepByName map[idutil.SafeID]ksaga.Step,
	compensateErrors *[]error,
) {
	for i := len(committed) - 1; i >= 0; i-- {
		if hbCtx.Err() != nil {
			return
		}
		cs := committed[i]
		step, found := stepByName[cs.name]
		if !found {
			c.logger.LogAttrs(hbCtx, slog.LevelWarn, "saga: compensation: unknown committed step name, skipping",
				sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
					slog.String("step_name", string(cs.name)))...)
			continue
		}
		if compensateErr := c.compensateOneStep(hbCtx, ci, step, cs.payload); compensateErr != nil {
			*compensateErrors = append(*compensateErrors, compensateErr)
		}
	}
}

// appendCompensationStarted appends KindCompensationStarted in a transaction.
func (c *Coordinator) appendCompensationStarted(ctx context.Context, ci journal.ClaimedInstance, runErr error) error {
	if err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		_, appErr := c.journal.Append(txCtx, ci.Instance.ID, ci.LeaseID, journal.Event{
			Kind:    journal.KindCompensationStarted,
			Payload: failurePayload(runErr),
		})
		return appErr
	}); err != nil {
		return fmt.Errorf("runCompensation: append KindCompensationStarted: %w", err)
	}
	return nil
}

// committedStepEntry holds a step name and its committed payload.
type committedStepEntry struct {
	name    idutil.SafeID
	payload []byte
}

// collectCommittedSteps collects committed steps eligible for reverse
// compensation. A step is eligible when KindStepCompleted appears in the
// event log AND no KindStepCompensated / KindStepCompensationFailed already
// records its compensate outcome (#1181 F2 — without this filter, recovery
// after a crash mid-rollback would re-run compensate on steps already
// rolled back, producing duplicate external side effects).
//
// priorFailureCount is the number of KindStepCompensationFailed events found
// in history. Steps with that kind are still excluded from the returned
// committed slice (idempotent signal: already attempted, not retried), but
// the count lets runCompensation seed compensateErrors so a recovery that
// finds all remaining work already done still terminates with
// StatusCompensationFailed rather than the incorrect StatusCompensated.
//
// ref: itimofeev/go-saga compensateErrors accumulation pattern.
//
// Returns committed entries in forward order; runCompensation walks them
// reverse. The name→Step index is built from def.Steps for compensate
// dispatch.
func collectCommittedSteps(events []journal.Event, def *ksaga.Definition) ([]committedStepEntry, map[idutil.SafeID]ksaga.Step, int) {
	// Mark every step name that already has a compensate outcome (success or
	// failure). Both kinds remove the step from the reverse-walk frontier:
	// once the executor's CompensateFunc has run we MUST NOT run it a second
	// time on the same persisted state, even if the first attempt failed —
	// the operator can rerun the saga or surface the failure via the
	// terminal StatusCompensationFailed projection.
	compensated := make(map[idutil.SafeID]struct{})
	var priorFailureCount int
	for i := range events {
		switch events[i].Kind {
		case journal.KindStepCompensated, journal.KindStepCompensationFailed:
			compensated[events[i].StepName] = struct{}{}
		}
		if events[i].Kind == journal.KindStepCompensationFailed {
			priorFailureCount++
		}
	}

	var committed []committedStepEntry
	for i := range events {
		if events[i].Kind != journal.KindStepCompleted {
			continue
		}
		if _, done := compensated[events[i].StepName]; done {
			continue
		}
		committed = append(committed, committedStepEntry{
			name:    events[i].StepName,
			payload: events[i].Payload,
		})
	}
	stepByName := make(map[idutil.SafeID]ksaga.Step, len(def.Steps))
	for _, s := range def.Steps {
		stepByName[s.Name] = s
	}
	return committed, stepByName, priorFailureCount
}

// compensateOneStep runs step.Compensate and records the outcome in the journal.
// Returns the compensation error if the compensate function fails (nil on success
// or when step.Compensate is nil). Journal append errors are logged but not
// returned — best-effort journaling keeps the reverse walk from aborting.
//
// Note: Compensate is not bounded by step.Timeout (deliberate, mirrors
// Temporal's lack of CompensateTimeout). The only bound is the saga-level ctx
// (parent deadline) plus the heartbeat goroutine's lease-loss cancel
// (~heartbeatInterval granularity). Authors needing a per-step compensation
// timeout should wrap with context.WithTimeout inside their CompensateFunc.
func (c *Coordinator) compensateOneStep(
	ctx context.Context,
	ci journal.ClaimedInstance,
	step ksaga.Step,
	committedPayload []byte,
) error {
	// #1181 F4: a step with no CompensateFunc has nothing to undo. Writing
	// KindStepCompensated for it would falsely claim a rollback happened —
	// downstream replay would treat the step as compensated when in fact the
	// forward side-effect is still in place. Skip both the executor call
	// AND the journal event; the step is structurally outside the reverse
	// walk's vocabulary.
	if step.Compensate == nil {
		return nil
	}
	compensateErr := c.executor.Compensate(ctx, &ci.Instance, ci.LeaseID, step, committedPayload)
	if compensateErr != nil {
		c.logger.LogAttrs(ctx, slog.LevelWarn, "saga: compensation: step compensate failed, continuing",
			sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("step_name", string(step.Name)),
				slog.Any("error", redaction.RedactAny(compensateErr)))...)
		if txErr := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			_, aErr := c.journal.Append(txCtx, ci.Instance.ID, ci.LeaseID, journal.Event{
				Kind:     journal.KindStepCompensationFailed,
				StepName: step.Name,
				Payload:  failurePayload(compensateErr),
			})
			return aErr
		}); txErr != nil {
			c.logger.LogAttrs(ctx, slog.LevelWarn, "saga: compensation: failed to append KindStepCompensationFailed",
				sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
					slog.String("definition_id", string(ci.Instance.DefinitionID)),
					slog.String("step_name", string(step.Name)),
					slog.Any("error", txErr))...)
		}
		return compensateErr
	}
	b, _ := json.Marshal(struct {
		Step string `json:"step"`
	}{Step: string(step.Name)})
	if txErr := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		_, aErr := c.journal.Append(txCtx, ci.Instance.ID, ci.LeaseID, journal.Event{
			Kind:     journal.KindStepCompensated,
			StepName: step.Name,
			Payload:  b,
		})
		return aErr
	}); txErr != nil {
		c.logger.LogAttrs(ctx, slog.LevelWarn, "saga: compensation: failed to append KindStepCompensated",
			sagalog.InstanceFields(ci.Instance.ID, ci.LeaseID,
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("step_name", string(step.Name)),
				slog.Any("error", txErr))...)
	}
	return nil
}

// commitStepArgs bundles the per-step commit inputs into a single value so
// commitStep / commitStepFailed / commitStepCompleted stay under the
// 7-parameter ceiling (driveOne knows all of these at the call site; bundling
// keeps the signature small and avoids accidental arg reordering).
type commitStepArgs struct {
	instanceID idutil.SafeID
	leaseID    idutil.SafeID
	defID      idutil.SafeID
	step       ksaga.Step
	newState   []byte
	runErr     error
	isLastStep bool
}

// commitStep records the step outcome atomically inside a transaction.
// On step failure it appends KindStepFailed and marks the instance terminal.
// On step success it appends KindStepCompleted, emits the step-completed
// outbox event, and (if it was the last step) marks the instance terminal.
func (c *Coordinator) commitStep(txCtx context.Context, a commitStepArgs) error {
	if a.runErr != nil {
		return c.commitStepFailed(txCtx, a)
	}
	return c.commitStepCompleted(txCtx, a)
}

func (c *Coordinator) commitStepFailed(txCtx context.Context, a commitStepArgs) error {
	if _, err := c.journal.Append(txCtx, a.instanceID, a.leaseID, journal.Event{
		Kind:     journal.KindStepFailed,
		StepName: a.step.Name,
		Payload:  failurePayload(a.runErr),
	}); err != nil {
		return err
	}
	_, err := c.journal.MarkTerminal(txCtx, a.instanceID, a.leaseID, ksaga.StatusFailed)
	return err
}

func (c *Coordinator) commitStepCompleted(txCtx context.Context, a commitStepArgs) error {
	if _, err := c.journal.Append(txCtx, a.instanceID, a.leaseID, journal.Event{
		Kind:     journal.KindStepCompleted,
		StepName: a.step.Name,
		Payload:  a.newState,
	}); err != nil {
		return err
	}
	if err := koutbox.Emit(txCtx, c.clock, c.outboxEmit,
		stepCompletedTopic(a.defID),
		StepCompletedEvent{InstanceID: a.instanceID, Step: a.step.Name}); err != nil {
		return err
	}
	if a.isLastStep {
		_, err := c.journal.MarkTerminal(txCtx, a.instanceID, a.leaseID, ksaga.StatusSucceeded)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// markTerminal helper
// ---------------------------------------------------------------------------

func (c *Coordinator) markTerminal(ctx context.Context, id, leaseID idutil.SafeID, finalStatus ksaga.Status) error {
	ok, err := c.journal.MarkTerminal(ctx, id, leaseID, finalStatus)
	if err != nil {
		return fmt.Errorf("MarkTerminal(%s): %w", finalStatus, err)
	}
	if !ok {
		c.logger.LogAttrs(ctx, slog.LevelWarn, "saga: MarkTerminal reported stale lease (ok=false)",
			sagalog.InstanceFields(id, leaseID,
				slog.String("final_status", finalStatus.String()))...)
	}
	return nil
}

// ---------------------------------------------------------------------------
// foldEvents — replay step cursor from history
// ---------------------------------------------------------------------------

// foldEvents reconstructs the step cursor from a replay of events.
//
//   - cursor = number of KindStepCompleted events seen
//   - prevState = Payload of the last KindStepCompleted event (nil if cursor == 0)
//
// Returns errFoldEventMismatch (using instanceID from the first StepFailed event
// found) if a KindStepFailed appears in the history — defensive, since the
// Journal should have MarkTerminal'd already.
func foldEvents(events []journal.Event, def *ksaga.Definition) (cursor int, prevState []byte, err error) {
	for i := range events {
		ev := &events[i]
		switch ev.Kind {
		case journal.KindStepCompleted:
			cursor++
			prevState = ev.Payload
		case journal.KindStepFailed:
			// Journal should have set the instance to terminal before we got here;
			// this is a defensive guard.
			return 0, nil, errFoldEventMismatch(ev.StepName,
				fmt.Sprintf("KindStepFailed in history at version %d", ev.Version))
		default:
			// KindStepStarted, compensation events, etc. — not used for cursor.
		}
	}
	_ = def // def not used in fold itself; passed for future per-step validation
	return cursor, prevState, nil
}

// ---------------------------------------------------------------------------
// StepCompletedEvent and helpers
// ---------------------------------------------------------------------------

// StepCompletedEvent is the outbox payload published after a successful step
// commit. It is emitted to the topic returned by stepCompletedTopic.
type StepCompletedEvent struct {
	InstanceID idutil.SafeID `json:"instanceId"`
	Step       idutil.SafeID `json:"step"`
}

// stepCompletedTopic returns the outbox topic for a step-completed event.
// Topic names are per-definition; Definition.ID is static Go code, not a
// runtime UUID, so codegen consumers (PR-07 contractgen) can compute the
// topic at compile time from the same definition ID constant.
func stepCompletedTopic(defID idutil.SafeID) string {
	return fmt.Sprintf("saga.%s.step_completed", defID)
}

// failurePayload returns a minimal JSON []byte capturing the error reason for
// journaling. Format: {"reason":"<truncated message>"}.
//
// For errcode.Error values, only the const-literal Message is used to avoid
// leaking runtime PII that may appear in InternalMessage or the Cause chain.
// Non-errcode errors are redacted via pkg/redaction.RedactString (masking
// key=value secrets / DSN / token) before truncation.
func failurePayload(err error) []byte {
	const maxReason = 256
	var ec *errcode.Error
	var reason string
	if errors.As(err, &ec) {
		reason = ec.Message // const literal only — no runtime PII
	} else {
		// Non-errcode error: err.Error() may carry runtime data (DSN, token,
		// key=value secrets) into the journal Payload. Redact before truncation
		// so a secret straddling maxReason cannot lose its mask anchor and leak
		// its tail (RedactString MUST precede the cap — observability.md
		// §"Span Attribute Redaction" ordering invariant).
		reason = redaction.RedactString(err.Error())
	}
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	b, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	return b
}
