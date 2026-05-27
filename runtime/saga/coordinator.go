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
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// Compile-time interface checks.
var _ healthz.RepoProber = (*Coordinator)(nil)

// ProbeCoordinatorReady is the healthz ProbeName for the saga coordinator
// journal readiness probe. Cell-side registration (RegisterReadiness) is the
// cell holder's responsibility (PR-09).
const ProbeCoordinatorReady healthz.ProbeName = "saga_coordinator_ready"

// UnsafeModeLabel is the slog field value emitted at Start() to alert operators
// that this Coordinator runs without distributed leader election.
const UnsafeModeLabel = "unsafe_no_leader"

// ---------------------------------------------------------------------------
// Coordinator lifecycle state machine
// ---------------------------------------------------------------------------

type coordState int32

const (
	coordStopped  coordState = iota // zero value = stopped
	coordStarting                   // Start() entered, goroutines launching
	coordRunning                    // tick + heartbeat loops active
	coordStopping                   // Stop() called, waiting for goroutines
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

const (
	defaultCoordPollInterval      = 200 * time.Millisecond
	defaultCoordClaimBatchSize    = 16
	defaultCoordLeaseDuration     = 30 * time.Second
	defaultCoordHeartbeatInterval = 10 * time.Second // = LeaseDuration / 3

	// minLeaseToHeartbeatRatio is the minimum factor by which LeaseDuration must
	// exceed HeartbeatInterval (LeaseDuration > HeartbeatInterval * ratio), so
	// at least one heartbeat fires before lease expiry. Extracted from the
	// inline literal per PROD-DURATION-CONST-01.
	minLeaseToHeartbeatRatio = 2
)

// Config holds tunable parameters for the Coordinator engine. Zero values are
// replaced by DefaultConfig() inside NewCoordinator.
type Config struct {
	// PollInterval is how often tickLoop calls ClaimPending. Default 200ms.
	PollInterval time.Duration
	// ClaimBatchSize is the maximum number of instances claimed per tick.
	// Default 16.
	ClaimBatchSize int
	// LeaseDuration is how long a claimed lease is held before the heartbeat
	// must extend it. Default 30s.
	LeaseDuration time.Duration
	// HeartbeatInterval is how often heartbeatLoop extends active leases.
	// Default 10s (= LeaseDuration/3). Must satisfy HeartbeatInterval*2 <
	// LeaseDuration so at least one heartbeat can fire before expiry.
	HeartbeatInterval time.Duration
}

// DefaultConfig returns a Config with documented defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:      defaultCoordPollInterval,
		ClaimBatchSize:    defaultCoordClaimBatchSize,
		LeaseDuration:     defaultCoordLeaseDuration,
		HeartbeatInterval: defaultCoordHeartbeatInterval,
	}
}

// Validate returns nil iff all duration fields are positive and
// HeartbeatInterval*2 < LeaseDuration.
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
	if c.HeartbeatInterval*minLeaseToHeartbeatRatio >= c.LeaseDuration {
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
// Coordinator is the only struct in this package that holds a journal.Journal
// field. Locked by SAGA-JOURNAL-HOLDER-SEAL-01 archtest (PR-08).
type Coordinator struct {
	// required deps — set by NewCoordinator, validated non-nil before opts loop
	journal    journal.Journal
	txRunner   persistence.TxRunner
	outboxEmit koutbox.Emitter
	registry   ksaga.Resolver

	// optional with defaults
	dispatcher Dispatcher   // default NoopDispatcher{}
	logger     *slog.Logger // default slog.Default()
	cfg        Config
	clock      clock.Clock

	// optional leader election (PR-05). nil locker → single-process unsafe
	// mode. leaderElectNil records a nil locker passed to WithLeaderElect so
	// NewCoordinator can fail-fast (strong-dependency wiring option). Held here
	// (NOT a journal.Journal field) so SAGA-JOURNAL-HOLDER-SEAL-01 is unaffected.
	locker         distlock.Locker
	leaderElectNil bool

	// lifecycle (mirrors runtime/outbox.Relay)
	state   atomic.Int32
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{}
	wg      sync.WaitGroup

	// activeLeases maps instanceID (idutil.SafeID) → inflightDrive for instances
	// currently being driven by driveOne. heartbeatLoop walks this map to extend
	// leases; Stop walks it to release in-flight distlocks on shutdown.
	activeLeases sync.Map
}

// inflightDrive is the activeLeases value: everything Stop/heartbeat need about
// an instance currently being driven. release frees the per-instance distlock
// (a no-op in single-process mode); it is idempotent (distlock Release is
// sync.Once-guarded) so calling it from both tickOnce and Stop is safe.
type inflightDrive struct {
	leaseID idutil.SafeID
	defID   idutil.SafeID
	release func()
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
		journal:    j,
		txRunner:   tx,
		outboxEmit: em,
		registry:   reg,
		clock:      clk,
		dispatcher: NoopDispatcher{},
		logger:     slog.Default(),
		cfg:        DefaultConfig(),
		readyCh:    make(chan struct{}),
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

// Start launches tickLoop + heartbeatLoop and blocks until ctx is canceled or
// Stop is called.
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
	c.wg.Add(2) // tickLoop + heartbeatLoop
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
	go func() { defer c.wg.Done(); c.heartbeatLoop(ctx) }()

	<-ctx.Done()
	return nil
}

// Stop signals shutdown, drains inflight driveOne goroutines, then cancels
// the loops and waits for them to exit. Idempotent.
//
// Drain budget = the passed-in ctx (same budget as the rest of Stop). While
// draining, tickOnce short-circuits to stop accepting new claims and
// heartbeatLoop keeps extending leases so inflight steps don't lose their
// lease mid-flight. If a non-cooperative Step.Run ignores ctx.Done() and
// exceeds the drain budget:
//
//   - cancel() fires anyway and Stop returns (best-effort).
//   - The orphaned step goroutine continues until it returns naturally; the
//     heartbeat goroutine has by then exited, so the journal lease expires.
//   - In leader-elect mode the per-instance distlock would otherwise keep
//     auto-renewing (it is decoupled from caller-ctx; see leader_elect.go), so
//     Stop explicitly releases every in-flight distlock (releaseInflightLocks).
//     Together with the expiring journal lease this lets another coordinator
//     re-claim the instance promptly — bounded takeover, matching the
//     etcd/redsync deadman-switch model — instead of stalling until this
//     process dies. Releasing while the orphaned step still runs is safe: its
//     commit is fenced by journal lease_id CAS (the PR-05 efficiency-lock model,
//     leader_elect.go).
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

	// Drain active leases before canceling goroutines. heartbeatLoop is still
	// running during this phase so leases stay valid. Non-cooperative steps
	// (those that ignore ctx) will continue until they naturally finish;
	// once driveOne returns, tickOnce deletes the lease from activeLeases.
	drainTicker := c.clock.NewTicker(c.cfg.PollInterval)
drain:
	for {
		select {
		case <-ctx.Done():
			break drain // budget exhausted; fall through to cancel
		case <-drainTicker.C():
			count := 0
			c.activeLeases.Range(func(_, _ any) bool { count++; return true })
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

	if cancel != nil {
		cancel()
	}
	// Release any in-flight distlocks so a wedged (non-cooperative) step cannot
	// hold its lock until process death. Cooperative steps already released via
	// tickOnce; this idempotently covers the drain-budget-exhausted case. The
	// journal lease_id CAS fences the orphaned step at commit.
	c.releaseInflightLocks()
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

// releaseInflightLocks frees the per-instance distlock for every drive still in
// activeLeases. Called from Stop after cancel so a non-cooperative step that
// outlives the drain budget cannot hold its lock until process death. release
// is idempotent (a no-op in single-process mode; distlock Release is
// sync.Once-guarded), so the owning tickOnce calling release again when it
// finally returns is harmless. Entries are left for the owning goroutine to
// delete from the map.
func (c *Coordinator) releaseInflightLocks() {
	c.activeLeases.Range(func(_, val any) bool {
		if d, ok := val.(inflightDrive); ok {
			d.release()
		}
		return true
	})
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
		return fmt.Errorf("ClaimPending: %w", err)
	}
	if len(claimed) == 0 {
		return nil
	}
	for _, ci := range claimed {
		// Leader-elect gate: in multi-process mode only the holder of the
		// per-instance distlock drives it; others skip this tick (no-lock →
		// skip). Single-process mode (no WithLeaderElect) always leads. This is
		// the sole driveOne call site, locked by SAGA-DRIVE-BEHIND-LEADER-GATE-01.
		release, lead := c.acquireLead(ctx, ci)
		if !lead {
			continue
		}
		// Use ci.LeaseID (per-instance fencing token) exclusively; the batch-level
		// leaseID from ClaimPending is discarded. PG Journal (PR-04) mints
		// per-instance tokens; using the batch token would break CAS fencing.
		c.activeLeases.Store(ci.Instance.ID, inflightDrive{
			leaseID: ci.LeaseID,
			defID:   ci.Instance.DefinitionID,
			release: release,
		})
		if err := c.driveOne(ctx, ci); err != nil {
			// Sentinel-aware severity: ErrSagaStaleLease (handoff race) →
			// Info; ErrSagaNotFound (instance gone) → Warn; default → Warn.
			// Keeps multi-coordinator deployments from spamming WARN
			// dashboards on every lease lost during normal handoff.
			c.logger.Log(ctx, journalErrLevel(err), "saga: drive failed",
				slog.String("instance_id", string(ci.Instance.ID)),
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("lease_id", string(ci.LeaseID)),
				slog.Any("error", err))
		}
		c.activeLeases.Delete(ci.Instance.ID)
		release()
	}
	return nil
}

// ---------------------------------------------------------------------------
// driveOne — core step execution
// ---------------------------------------------------------------------------

func (c *Coordinator) driveOne(ctx context.Context, ci journal.ClaimedInstance) error {
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
	cursor, prevState, foldErr := foldEvents(events, def)
	if foldErr != nil {
		c.logger.WarnContext(ctx, "saga: fold failed, marking terminal",
			slog.String("instance_id", string(ci.Instance.ID)),
			slog.String("definition_id", string(ci.Instance.DefinitionID)),
			slog.Any("error", foldErr))
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusFailed)
	}
	if cursor >= def.Len() {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusSucceeded)
	}

	// 4. Run step OUTSIDE tx. safeRun recovers panics.
	// Derive a per-step context with deadline from def.Timeout and/or step.Timeout.
	// Deadlines use absolute wall-clock time derived from the injected clock so
	// tests using a FakeClock can control time precisely. Each WithDeadline
	// cancel func is defer-released so they fire deterministically once driveOne
	// returns (LIFO order — innermost step cancel first, then total).
	nextStep := def.Steps[cursor]
	runCtx := ctx
	if def.Timeout > 0 {
		totalDeadline := ci.Instance.StartedAt.Add(def.Timeout)
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(ctx, totalDeadline)
		defer cancel()
	}
	if nextStep.Timeout > 0 {
		stepDeadline := c.clock.Now().Add(nextStep.Timeout)
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(runCtx, stepDeadline)
		defer cancel()
	}
	newState, runErr := safeRun(runCtx, nextStep.Run, &ci.Instance, prevState)
	// If the derived deadline ctx fired (def.Timeout or step.Timeout exceeded)
	// while the parent ctx is still valid, treat the outcome as a saga timeout.
	// Two paths reach here:
	//   (a) step returned nil despite its ctx being canceled (non-cooperative
	//       step that ignored ctx.Done()) → runErr == nil
	//   (b) step observed ctx.Err() and returned an error derived from it →
	//       runErr != nil but the cause is the derived deadline, not a domain
	//       failure
	// Either way, mark Expired (not Failed). Use the parent ctx for markTerminal
	// so the journal write is not pre-canceled by the deadline that just fired.
	if runCtx.Err() != nil && ctx.Err() == nil {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusExpired)
	}

	// 5. Open short tx: Append + (maybe) Emit + RegisterAfterCommit.
	args := commitStepArgs{
		instanceID: ci.Instance.ID,
		leaseID:    ci.LeaseID,
		defID:      def.ID,
		step:       nextStep,
		newState:   newState,
		runErr:     runErr,
		isLastStep: cursor == def.Len()-1,
	}
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
	if err := koutbox.Emit(txCtx, c.outboxEmit,
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
// heartbeatLoop
// ---------------------------------------------------------------------------

func (c *Coordinator) heartbeatLoop(ctx context.Context) {
	ticker := c.clock.NewTicker(c.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			c.heartbeatOnce(ctx)
		}
	}
}

// heartbeatOnce extends all active leases tracked in activeLeases.
// Stale leases (ok=false) are removed from the map.
func (c *Coordinator) heartbeatOnce(ctx context.Context) {
	c.activeLeases.Range(func(key, val any) bool {
		instanceID, ok1 := key.(idutil.SafeID)
		d, ok2 := val.(inflightDrive)
		if !ok1 || !ok2 {
			return true
		}
		ok, err := c.journal.Heartbeat(ctx, instanceID, d.leaseID, c.cfg.LeaseDuration)
		if err != nil {
			// Heartbeat returns (false, nil) on stale lease or missing
			// instance by contract — any err here is real infra (PG
			// outage, ctx cancel) or KindInvalid (programmer error). Both
			// stay at Warn; classifier still routes if a future Journal
			// impl widens the error shape.
			c.logger.Log(ctx, journalErrLevel(err), "saga: heartbeat failed",
				slog.String("instance_id", string(instanceID)),
				slog.String("definition_id", string(d.defID)),
				slog.Any("error", err))
			return true
		}
		if !ok {
			// Stale lease — stop tracking; another leader claimed it.
			c.activeLeases.Delete(instanceID)
		}
		return true
	})
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
		c.logger.WarnContext(ctx, "saga: MarkTerminal reported stale lease (ok=false)",
			slog.String("instance_id", string(id)),
			slog.String("lease_id", string(leaseID)),
			slog.String("final_status", finalStatus.String()))
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
// safeRun — panic-guarded step executor
// ---------------------------------------------------------------------------

// safeRun calls fn and converts panics to errors. safeRun MUST be the only
// callsite of StepFunc inside this package (locked by SAGA-STEP-RUN-OUTSIDE-TX-01
// archtest shipped in PR-08).
//
// ctx carries the derived step/saga deadline; a step that ignores ctx.Done()
// will block this goroutine until it returns naturally. The Coordinator
// detects post-call ctx expiry (driveOne) and marks the instance Expired,
// but cannot terminate the underlying goroutine — see ksaga.StepFunc godoc
// for the cooperative-cancellation contract.
func safeRun(ctx context.Context, fn ksaga.StepFunc, inst *ksaga.Instance, prev []byte) (newState []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errcode.New(errcode.KindInternal, errcode.ErrInternal,
				"saga: step panicked",
				errcode.WithDetails(errcode.PublicString("instanceId", string(inst.ID))),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("panic: %v", r))))
		}
	}()
	return fn(ctx, inst, prev)
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
