package saga

import (
	"context"
	"encoding/json"
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
)

// Compile-time interface checks.
var _ healthz.RepoProber = (*Coordinator)(nil)

// ProbeCoordinatorReady is the healthz ReadyProbeName for the saga coordinator
// journal readiness probe. Cell-side registration (RegisterRepoReady) is the
// cell holder's responsibility (PR-09).
const ProbeCoordinatorReady healthz.ReadyProbeName = "saga_coordinator_ready"

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
// This implementation runs in a single process with NO distributed leader
// election (unsafe mode). Start() emits slog.Warn(mode="unsafe_no_leader").
// PR-05 will add distlock-based leader election.
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
	registry   ksaga.Registry

	// optional with defaults
	dispatcher Dispatcher   // default NoopDispatcher{}
	logger     *slog.Logger // default slog.Default()
	cfg        Config
	clock      clock.Clock

	// lifecycle (mirrors runtime/outbox.Relay)
	state   atomic.Int32
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{}
	wg      sync.WaitGroup

	// activeLeases maps instanceID → leaseID for instances currently being
	// driven by driveOne. heartbeatLoop walks this map to extend leases.
	activeLeases sync.Map
}

// NewCoordinator validates required deps and applies opts. Nil required deps
// return errcode.KindInvalid + ErrValidationFailed. A nil or typed-nil clock
// panics via clock.MustHaveClock (programmer error).
func NewCoordinator(
	j journal.Journal,
	tx persistence.TxRunner,
	em koutbox.Emitter,
	reg ksaga.Registry,
	clk clock.Clock,
	opts ...Option,
) (*Coordinator, error) {
	if j == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: journal required")
	}
	if tx == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: txRunner required")
	}
	if em == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga: outboxEmit required")
	}
	if reg == nil {
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
	if err := c.cfg.Validate(); err != nil {
		return nil, err
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

	c.logger.WarnContext(ctx, "saga coordinator: started in UNSAFE single-process mode (no leader-elect)",
		slog.String("mode", UnsafeModeLabel))

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

// Stop signals shutdown and waits for goroutines to drain. Idempotent.
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

	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.mu.Unlock()

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

// Ready returns a channel that is closed once Start transitions to running.
func (c *Coordinator) Ready() <-chan struct{} {
	c.mu.Lock()
	ch := c.readyCh
	c.mu.Unlock()
	return ch
}

// RepoReady implements healthz.RepoProber by delegating to journal.RepoReady.
// Cell-side registration (RegisterRepoReady funnel) is the Coordinator cell
// holder's responsibility (PR-09).
func (c *Coordinator) RepoReady(ctx context.Context) error {
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
	claimed, _, err := c.journal.ClaimPending(ctx, c.cfg.ClaimBatchSize, c.cfg.LeaseDuration)
	if err != nil {
		return fmt.Errorf("ClaimPending: %w", err)
	}
	if len(claimed) == 0 {
		return nil
	}
	for _, ci := range claimed {
		// Use ci.LeaseID (per-instance fencing token) exclusively; the batch-level
		// leaseID from ClaimPending is discarded. PG Journal (PR-04) mints
		// per-instance tokens; using the batch token would break CAS fencing.
		c.activeLeases.Store(ci.Instance.ID, ci.LeaseID)
		if err := c.driveOne(ctx, ci); err != nil {
			c.logger.WarnContext(ctx, "saga: drive failed",
				slog.String("instance_id", string(ci.Instance.ID)),
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("lease_id", string(ci.LeaseID)),
				slog.Any("error", err))
		}
		c.activeLeases.Delete(ci.Instance.ID)
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
	nextStep := def.Steps[cursor]
	newState, runErr := safeRun(ctx, nextStep.Run, &ci.Instance, prevState)

	// 5. Open short tx: Append + (maybe) Emit + RegisterAfterCommit.
	isLastStep := cursor == def.Len()-1
	return c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := c.commitStep(txCtx, ci.Instance.ID, ci.LeaseID, def.ID, nextStep, newState, runErr, isLastStep); err != nil {
			return err
		}
		persistence.RegisterAfterCommit(txCtx, func(hookCtx context.Context) {
			c.dispatcher.Kick(hookCtx)
		})
		return nil
	})
}

// commitStep records the step outcome atomically inside a transaction.
// On step failure it appends KindStepFailed and marks the instance terminal.
// On step success it appends KindStepCompleted, emits the step-completed
// outbox event, and (if it was the last step) marks the instance terminal.
func (c *Coordinator) commitStep(
	txCtx context.Context,
	instanceID, leaseID, defID idutil.SafeID,
	step ksaga.Step,
	newState []byte,
	runErr error,
	isLastStep bool,
) error {
	if runErr != nil {
		return c.commitStepFailed(txCtx, instanceID, leaseID, step.Name, runErr)
	}
	return c.commitStepCompleted(txCtx, instanceID, leaseID, defID, step.Name, newState, isLastStep)
}

func (c *Coordinator) commitStepFailed(
	txCtx context.Context,
	instanceID, leaseID, stepName idutil.SafeID,
	runErr error,
) error {
	if _, err := c.journal.Append(txCtx, instanceID, leaseID, journal.Event{
		Kind:     journal.KindStepFailed,
		StepName: stepName,
		Payload:  failurePayload(runErr),
	}); err != nil {
		return err
	}
	_, err := c.journal.MarkTerminal(txCtx, instanceID, leaseID, ksaga.StatusFailed)
	return err
}

func (c *Coordinator) commitStepCompleted(
	txCtx context.Context,
	instanceID, leaseID, defID, stepName idutil.SafeID,
	newState []byte,
	isLastStep bool,
) error {
	if _, err := c.journal.Append(txCtx, instanceID, leaseID, journal.Event{
		Kind:     journal.KindStepCompleted,
		StepName: stepName,
		Payload:  newState,
	}); err != nil {
		return err
	}
	if err := koutbox.Emit(txCtx, c.outboxEmit,
		stepCompletedTopic(defID),
		StepCompletedEvent{InstanceID: instanceID, Step: stepName}); err != nil {
		return err
	}
	if isLastStep {
		_, err := c.journal.MarkTerminal(txCtx, instanceID, leaseID, ksaga.StatusSucceeded)
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
		leaseID, ok2 := val.(idutil.SafeID)
		if !ok1 || !ok2 {
			return true
		}
		ok, err := c.journal.Heartbeat(ctx, instanceID, leaseID, c.cfg.LeaseDuration)
		if err != nil {
			c.logger.WarnContext(ctx, "saga: heartbeat failed",
				slog.String("instance_id", string(instanceID)),
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
func safeRun(ctx context.Context, fn ksaga.StepFunc, inst *ksaga.Instance, prev []byte) (newState []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errcode.New(errcode.KindInternal, errcode.ErrInternal,
				"saga: step panicked",
				errcode.WithDetails(slog.String("instanceId", string(inst.ID))),
				errcode.WithInternal(fmt.Sprintf("panic: %v", r)))
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
// journaling. Format: {"reason":"<truncated err.Error()>"}.
func failurePayload(err error) []byte {
	const maxReason = 256
	reason := err.Error()
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	b, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	return b
}
