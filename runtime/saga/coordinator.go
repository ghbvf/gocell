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
	"github.com/ghbvf/gocell/runtime/saga/executor"
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

// Config holds tunable parameters for the Coordinator engine. Zero values are
// replaced by DefaultConfig() inside NewCoordinator.
type Config struct {
	// PollInterval is how often tickLoop calls ClaimPending. Default 200ms.
	PollInterval time.Duration
	// ClaimBatchSize is the maximum number of instances claimed per tick.
	// Default 16.
	ClaimBatchSize int
	// LeaseDuration is how long a claimed lease is held. Default 30s.
	// Also used as the per-instance distlock TTL in leader-elect mode.
	LeaseDuration time.Duration
}

// DefaultConfig returns a Config with documented defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:   defaultCoordPollInterval,
		ClaimBatchSize: defaultCoordClaimBatchSize,
		LeaseDuration:  defaultCoordLeaseDuration,
	}
}

// Validate returns nil iff all fields are positive.
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

	// executor is the required per-step execution engine. Injected via
	// WithExecutor. executorNil records a typed-nil passed to WithExecutor
	// so NewCoordinator can fail-fast (strong-dependency wiring option).
	executor    *executor.Executor
	executorNil bool

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
// instance currently being driven. release frees the per-instance distlock
// (a no-op in single-process mode); it is idempotent (distlock Release is
// sync.Once-guarded) so calling it from both tickOnce and Stop is safe.
type inflightDrive struct {
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
	if c.executorNil || c.executor == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: executor required; pass a non-nil *executor.Executor via WithExecutor")
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
//   - In leader-elect mode the per-instance distlock would otherwise keep
//     auto-renewing, so Stop explicitly releases every in-flight distlock
//     (releaseInflightLocks). Together with the expiring journal lease this lets
//     another coordinator re-claim the instance promptly — bounded takeover,
//     matching the etcd/redsync deadman-switch model. Releasing while the orphaned
//     step still runs is safe: its commit is fenced by journal lease_id CAS
//     (the PR-05 efficiency-lock model, leader_elect.go).
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
// inflightLocks. Called from Stop after cancel so a non-cooperative step that
// outlives the drain budget cannot hold its lock until process death. release
// is idempotent (a no-op in single-process mode; distlock Release is
// sync.Once-guarded), so the owning tickOnce calling release again when it
// finally returns is harmless. Entries are left for the owning goroutine to
// delete from the map.
func (c *Coordinator) releaseInflightLocks() {
	c.inflightLocks.Range(func(_, val any) bool {
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
		c.inflightLocks.Store(ci.Instance.ID, inflightDrive{
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
		c.inflightLocks.Delete(ci.Instance.ID)
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
		c.logger.InfoContext(ctx, "saga: lease lost during step run; another leader took over",
			slog.String("instance_id", string(ci.Instance.ID)),
			slog.String("definition_id", string(def.ID)),
			slog.String("lease_id", string(ci.LeaseID)))
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
// steps compensated cleanly, Failed otherwise.
//
// ref: itimofeev/go-saga coordinator.go abort() — best-effort reverse
// compensation with error aggregation.
func (c *Coordinator) runCompensation(
	ctx context.Context,
	ci journal.ClaimedInstance,
	def *ksaga.Definition,
	events []journal.Event,
	runErr error,
) error {
	// Step a: append KindCompensationStarted in tx to push status → Compensating.
	if err := c.appendCompensationStarted(ctx, ci, runErr); err != nil {
		return err
	}

	// Step b: collect committed steps + build step index.
	committed, stepByName := collectCommittedSteps(events, def)

	// Step c: reverse-walk under heartbeat.
	var compensateErrors []error
	walkErr := c.executor.RunWithHeartbeat(ctx, &ci.Instance, ci.LeaseID, func(hbCtx context.Context) error {
		for i := len(committed) - 1; i >= 0; i-- {
			if hbCtx.Err() != nil {
				break // lease lost or parent canceled; stop accepting new compensate work
			}
			cs := committed[i]
			step, found := stepByName[cs.name]
			if !found {
				c.logger.WarnContext(hbCtx, "saga: compensation: unknown committed step name, skipping",
					slog.String("instance_id", string(ci.Instance.ID)),
					slog.String("step_name", string(cs.name)))
				continue
			}
			if compensateErr := c.compensateOneStep(hbCtx, ci, step, cs.payload); compensateErr != nil {
				compensateErrors = append(compensateErrors, compensateErr)
			}
		}
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
	if len(compensateErrors) > 0 {
		return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusFailed)
	}
	return c.markTerminal(ctx, ci.Instance.ID, ci.LeaseID, ksaga.StatusCompensated)
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

// collectCommittedSteps collects all KindStepCompleted events in forward order
// and builds a name→Step index. Extracted from runCompensation to reduce complexity.
func collectCommittedSteps(events []journal.Event, def *ksaga.Definition) ([]committedStepEntry, map[idutil.SafeID]ksaga.Step) {
	var committed []committedStepEntry
	for i := range events {
		if events[i].Kind == journal.KindStepCompleted {
			committed = append(committed, committedStepEntry{
				name:    events[i].StepName,
				payload: events[i].Payload,
			})
		}
	}
	stepByName := make(map[idutil.SafeID]ksaga.Step, len(def.Steps))
	for _, s := range def.Steps {
		stepByName[s.Name] = s
	}
	return committed, stepByName
}

// compensateOneStep runs step.Compensate and records the outcome in the journal.
// Returns the compensation error if the compensate function fails (nil on success
// or when step.Compensate is nil). Journal append errors are logged but not
// returned — best-effort journaling keeps the reverse walk from aborting.
func (c *Coordinator) compensateOneStep(
	ctx context.Context,
	ci journal.ClaimedInstance,
	step ksaga.Step,
	committedPayload []byte,
) error {
	compensateErr := c.executor.Compensate(ctx, &ci.Instance, step, committedPayload)
	if compensateErr != nil {
		c.logger.WarnContext(ctx, "saga: compensation: step compensate failed, continuing",
			slog.String("instance_id", string(ci.Instance.ID)),
			slog.String("definition_id", string(ci.Instance.DefinitionID)),
			slog.String("step_name", string(step.Name)),
			slog.Any("error", compensateErr))
		if txErr := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			_, aErr := c.journal.Append(txCtx, ci.Instance.ID, ci.LeaseID, journal.Event{
				Kind:     journal.KindStepFailed,
				StepName: step.Name,
				Payload:  failurePayload(compensateErr),
			})
			return aErr
		}); txErr != nil {
			c.logger.WarnContext(ctx, "saga: compensation: failed to append KindStepFailed",
				slog.String("instance_id", string(ci.Instance.ID)),
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("step_name", string(step.Name)),
				slog.Any("error", txErr))
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
		c.logger.WarnContext(ctx, "saga: compensation: failed to append KindStepCompensated",
			slog.String("instance_id", string(ci.Instance.ID)),
			slog.String("definition_id", string(ci.Instance.DefinitionID)),
			slog.String("step_name", string(step.Name)),
			slog.Any("error", txErr))
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
