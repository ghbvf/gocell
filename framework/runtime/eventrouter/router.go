// Package eventrouter provides a Router that separates event subscription
// declaration from execution. Cells declare handlers via AddContractHandler; the
// Router owns goroutine lifecycle, setup-error detection, and graceful shutdown.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddContractHandler/Run/Close pattern.
// Adopted: declaration-then-run split, Running() readiness signal, WaitGroup goroutine tracking.
// Deviated: no publish-side in AddContractHandler (GoCell publishes via outbox.Writer, not Router);
// startup readiness uses an explicit Ready signal from the Subscriber interface rather
// than a timeout heuristic — aligned with Uber fx OnStart synchronous return semantics.
//
// Run lifecycle (4 phases):
//
//	Phase 1: serially call Subscriber.Setup(sub) for each handler — blocking topology declaration.
//	Phase 2: launch one Subscribe goroutine per handler concurrently.
//	Phase 3: wait on Subscriber.Ready(sub) for every handler (explicit signal, no timeout).
//	Phase 4: block until ctx cancel or a runtime subscription error.
package eventrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/observability"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// subscribeFailure carries a Phase 2 subscription error from the producer
// goroutine in runSubscribe to the Phase 3 or Phase 4 consumer point. The
// producer never decides which phase classification to record — that is the
// consumer's responsibility, owned by phase context. This pattern eliminates
// the producer-side race between `select <-r.running` and `closeRunning()`
// that previously caused reason flicker between subscribe_failure and
// runtime_fault.
//
// The reason field is populated by the producer with the *setup-phase* reason
// (SetupErrorReasonSubscribeFailure or SetupErrorReasonPanic) and is consumed
// only by the Phase 3 consumer. The Phase 4 consumer always records
// RuntimeErrorReasonRuntimeFault and ignores reason — by Phase 4 the failure
// is by definition a runtime fault regardless of its underlying cause.
//
// ref: PR #593 review fix-up (P1#1 reason flicker, P1#2 panic redaction).
type subscribeFailure struct {
	cellID string
	topic  string
	reason string
	err    error
}

func (sf subscribeFailure) Error() string { return sf.err.Error() }
func (sf subscribeFailure) Unwrap() error { return sf.err }

// errSubscriptionPanicked is the fixed error returned on the recover() path. The
// original panic value never enters the error chain so it cannot leak via
// Health() / Run() / slog "error" fields. The redacted form of the panic
// value is logged separately as a typed slog string field
// "recovered_redacted", scrubbed by pkg/redaction.RedactString.
var errSubscriptionPanicked = errors.New("eventrouter: subscription panicked (redacted)")

// DefaultReadyTimeout bounds Phase 3 of Run() so a subscriber that never
// signals Ready (broker reconnect storm, mis-configured topology) does not
// block bootstrap indefinitely. 30s aligns with Uber fx StartTimeout default.
// Set to a non-positive value via WithReadyTimeout to disable the bound.
const DefaultReadyTimeout = 30 * time.Second

// phaseError wraps an error with the name of the shutdown phase that produced
// it. This makes post-mortem diagnosis unambiguous when multiple phases can
// fail and the error is logged or inspected via errors.As.
//
// Phase labels mirror the Close comment block:
//
//	"stop_intake"   — Phase 1: StopIntake ctx budget exceeded
//	"wg_wait"       — Phase 3: goroutine drain timed out
type phaseError struct {
	Phase string
	Err   error
}

func (e *phaseError) Error() string { return e.Phase + ": " + e.Err.Error() }
func (e *phaseError) Unwrap() error { return e.Err }

// Option configures a Router.
type Option func(*Router)

// WithReadyTimeout overrides the default ready-wait budget. A value <= 0
// disables the timeout (waits indefinitely on Ready channels and ctx).
func WithReadyTimeout(d time.Duration) Option {
	return func(r *Router) { r.readyTimeout = d }
}

// WithEventRouterCollector wires an EventCollector into the Router so that
// subscription lifecycle events (setup errors, ready-wait durations, active
// counts) are recorded. Typed-nil inputs are silently ignored; the final
// fallback to NopEventCollector{} occurs at New() after all options are applied.
//
// Follows the cumulative builder noop pattern (runtime-api.md §Option 范式分层):
// nil input ≠ "remove the collector", it means "no change this call".
func WithEventRouterCollector(c EventCollector) Option {
	return func(r *Router) {
		if validation.IsNilInterface(c) {
			return
		}
		r.collector = c
	}
}

type handlerConfig struct {
	topic               string
	handler             outbox.EntryHandler
	consumerGroup       string
	cellID              string // cellID is the observability owner; must be provided by caller — no fallback to consumerGroup (K#07).
	sliceID             string
	contract            contractspec.ContractSpec
	brokerDelaySchedule []time.Duration // #1458: per-attempt webhook retry delays (empty for ordinary subscriptions).
}

// Router manages event subscription lifecycle. It is populated from
// RegistrySnapshot.Subscriptions drained by bootstrap phase6, and provides
// Run/Close for the execution phase.
//
// Run MUST be called at most once. Calling Run a second time returns an error.
//
// The subscriber field is *outbox.SubscriberWithMiddleware so that runSubscribe
// can call SubscribeEntry (EntryHandler → business middleware chain →
// ConsumerBase.Wrap → Inner.Subscribe). The EntryToSubscriberHandler lift
// function has been deleted; SubscribeEntry is the only public entry point,
// which is the structural guarantee that ConsumerBase is the explicit
// conversion boundary between business (EntryHandler) and broker
// (SubscriberHandler) layers.
//
// ref: ThreeDotsLabs/watermill message/router.go — router holds *Router.handlers,
// not a generic middleware list: the router owns the composition.
type Router struct {
	subscriber   *outbox.SubscriberWithMiddleware
	handlers     []handlerConfig
	validators   []cell.SubscriptionValidator
	mu           sync.Mutex
	readyTimeout time.Duration
	running      chan struct{}
	runGuard     sync.Once // ensures Run is called at most once
	runningOnce  sync.Once // ensures close(r.running) is called at most once
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	statusMu     sync.RWMutex
	started      bool
	shutdown     bool
	healthErr    error
	clock        clock.Clock
	collector    EventCollector
	activeMu     sync.Mutex
	activeCells  []string // cellIDs of handlers that have been Inc'd; used by Close to Dec
}

// Compile-time interface checks.
var _ cell.SubscriptionValidatorAdder = (*Router)(nil)

// New creates a Router that will use the given SubscriberWithMiddleware for all
// subscriptions. The subscriber field is typed as *outbox.SubscriberWithMiddleware
// so that runSubscribe can call SubscribeEntry and get the full business pipeline:
// business middleware chain → ConsumerBase.Wrap → Inner.Subscribe.
//
// clk is required; pass clock.Real() at the composition root or
// clockmock.New(...) in tests. Panics on nil or typed-nil clock.
func New(sub *outbox.SubscriberWithMiddleware, clk clock.Clock, opts ...Option) *Router {
	clock.MustHaveClock(clk, "eventrouter.New")
	r := &Router{
		subscriber:   sub,
		readyTimeout: DefaultReadyTimeout,
		running:      make(chan struct{}),
		clock:        clk,
	}
	for _, o := range opts {
		o(r)
	}
	if r.collector == nil {
		r.collector = NopEventCollector{}
	}
	return r
}

// AddContractHandler registers a contract-first subscription intent. The
// Router stores the contract metadata on the Subscription; bootstrap decorates
// the concrete Subscriber with NewContractTracingSubscriber so delivery spans
// close after final broker settlement.
//
// spec.Kind MUST be "event" and spec.Topic MUST be set. topic used for the
// Subscriber.Setup / Subscribe lifecycle is derived from spec.Topic — callers
// do not pass a separate topic string.
//
// ownerCellID is the cell that owns this subscription — distinct from
// consumerGroup (which may include a role suffix like
// "accesscore-rbac-session-sync"). It is mandatory and populates
// Subscription.CellID directly; there is no fallback to consumerGroup
// because Registry.Subscribe forces cellID to flow positionally from cell
// metadata at codegen time (HARD contract).
//
// Returns a non-nil error when handler is nil, consumerGroup is empty,
// ownerCellID is empty, or the spec is malformed; callers should propagate
// the error to the bootstrap phase6 subscription walker.
//
// ref: ThreeDotsLabs/watermill router.AddHandler handlerName / NATS subscription metadata.
// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
func (r *Router) AddContractHandler(
	spec contractspec.ContractSpec, handler outbox.EntryHandler, consumerGroup string, ownerCellID string, opts ...cell.SubscriptionOption,
) error {
	if handler == nil {
		return fmt.Errorf("eventrouter: AddContractHandler called with nil handler")
	}
	if consumerGroup == "" {
		return fmt.Errorf("eventrouter: AddContractHandler called with empty consumerGroup; cells must declare their identity")
	}
	if ownerCellID == "" {
		return fmt.Errorf("eventrouter: AddContractHandler called with empty ownerCellID; " +
			"cellID is HARD-required (positional parameter on Registry.Subscribe, injected by codegen)")
	}
	if spec.Kind != "event" {
		return fmt.Errorf("eventrouter: Contract.Kind %q must be \"event\"", spec.Kind)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("eventrouter: AddContractHandler: %w", err)
	}
	req := cell.SubscriptionRequest{
		Spec:          spec,
		Handler:       handler,
		ConsumerGroup: consumerGroup,
		CellID:        ownerCellID,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&req)
		}
	}

	// Build a representative Subscription so validators can inspect all fields.
	// Validators run outside the lock to avoid holding the subscription store
	// during potentially slow or user-provided checks.
	candidateSub := outbox.Subscription{
		Topic:               spec.Topic,
		ConsumerGroup:       consumerGroup,
		CellID:              ownerCellID,
		SliceID:             req.SliceID,
		BrokerDelaySchedule: req.BrokerDelaySchedule,
	}
	r.mu.Lock()
	currentValidators := make([]cell.SubscriptionValidator, len(r.validators))
	copy(currentValidators, r.validators)
	r.mu.Unlock()

	var validationErrs []error
	for _, v := range currentValidators {
		if v == nil {
			continue
		}
		if err := v(candidateSub); err != nil {
			validationErrs = append(validationErrs, err)
		}
	}
	if joined := errors.Join(validationErrs...); joined != nil {
		return fmt.Errorf("eventrouter: subscription validation failed for topic %q: %w", spec.Topic, joined)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, handlerConfig{
		topic:               spec.Topic,
		handler:             handler,
		consumerGroup:       consumerGroup,
		cellID:              ownerCellID,
		sliceID:             req.SliceID,
		contract:            spec,
		brokerDelaySchedule: req.BrokerDelaySchedule,
	})
	return nil
}

// AddSubscriptionValidator registers a validator that is invoked for every
// subsequent AddContractHandler call. Validators run outside the Router lock so
// a slow or panicking validator does not hold the subscription store.
// A nil validator is silently ignored.
func (r *Router) AddSubscriptionValidator(v cell.SubscriptionValidator) {
	if v == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.validators = append(r.validators, v)
}

// errAlreadyRunning is returned if Run is called more than once.
var errAlreadyRunning = fmt.Errorf("eventrouter: Run called more than once")

// Run starts all registered subscriptions and blocks until ctx is canceled
// or an unrecoverable subscription error occurs.
//
// Run MUST be called at most once; a second call returns errAlreadyRunning.
//
// Lifecycle (4 phases):
//
//	Phase 1: serially call Subscriber.Setup(sub) — blocking topology declaration.
//	         Any Setup error causes Run to return immediately without starting subscriptions.
//	Phase 2: launch one Subscribe goroutine per handler concurrently.
//	Phase 3: wait on Subscriber.Ready(sub) for every handler.
//	         Running() is closed only after ALL Ready channels close.
//	Phase 4: block until ctx cancel or a runtime subscription error.
//
// On context cancellation, Run waits for all goroutines to finish before returning.
func (r *Router) Run(ctx context.Context) error {
	var firstRun bool
	r.runGuard.Do(func() { firstRun = true })
	if !firstRun {
		return errAlreadyRunning
	}

	r.mu.Lock()
	handlers := make([]handlerConfig, len(r.handlers))
	copy(handlers, r.handlers)
	r.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	if len(handlers) == 0 {
		r.markRunning()
		r.closeRunning()
		<-runCtx.Done()
		cancel()
		return nil
	}

	// Phase 1: serially Setup each subscription (topology declaration).
	if err := r.runSetup(runCtx, cancel, handlers); err != nil {
		return err
	}

	// setupErr receives the first subscription failure as a typed sentinel.
	// Buffer size = len(handlers) to avoid goroutine leaks if multiple fail.
	setupErr := make(chan subscribeFailure, len(handlers))

	// Phase 2: launch Subscribe goroutines concurrently.
	r.runSubscribe(runCtx, handlers, setupErr)

	// Phase 3: wait for all Ready signals before marking Running.
	if err := r.runAwaitReady(runCtx, cancel, handlers, setupErr); err != nil {
		return err
	}

	r.markRunning()
	slog.Info("eventrouter: all subscriptions ready (explicit Ready signal)",
		slog.Int("count", len(handlers)))
	r.closeRunning()

	// Phase 4: block until context canceled or a runtime error surfaces.
	// The Phase 4 consumer always records runtime_fault — by Phase 4 the
	// router is running, so any failure is a runtime fault regardless of
	// the underlying cause. The reason field on the sentinel is ignored
	// here (it belongs to the Phase 3 consumer).
	select {
	case <-runCtx.Done():
		r.markShutdown()
	case sf := <-setupErr:
		err := fmt.Errorf("eventrouter: topic %s: %w", sf.topic, sf.err)
		r.markHealthError(err)
		slog.Error("eventrouter: subscription failed at runtime",
			slog.String("cell_id", sf.cellID),
			slog.String("topic", sf.topic),
			slog.Any("error", sf.err))
		observability.SafeObserve(slog.Default(), func() {
			r.collector.RecordRuntimeError(runCtx, sf.cellID, sf.topic, RuntimeErrorReasonRuntimeFault)
		})
		cancel()
		r.wg.Wait()
		return err
	}

	r.wg.Wait()
	return nil
}

// runSetup calls Subscriber.Setup for each handler sequentially (Phase 1).
// On error, it cancels the context and returns the wrapped error.
func (r *Router) runSetup(ctx context.Context, cancel context.CancelFunc, handlers []handlerConfig) error {
	for _, h := range handlers {
		sub := h.subscription()
		if err := r.subscriber.Setup(ctx, sub); err != nil {
			wrapped := fmt.Errorf("eventrouter: setup %s: %w", sub.Topic, err)
			r.markHealthError(wrapped)
			slog.Error("eventrouter: subscription setup failed, aborting",
				slog.String("topic", sub.Topic),
				slog.String("consumer_group", sub.ConsumerGroup),
				slog.String("cell_id", sub.CellID),
				slog.Any("error", err))
			observability.SafeObserve(slog.Default(), func() {
				r.collector.RecordSetupError(ctx, sub.CellID, sub.Topic, SetupErrorReasonSetupError)
			})
			cancel()
			return wrapped
		}
	}
	return nil
}

// runSubscribe starts one goroutine per handler that calls SubscribeEntry
// (Phase 2). Failures are forwarded as typed subscribeFailure sentinels to
// the Phase 3 / Phase 4 consumer point of setupErr; the producer goroutine
// MUST NOT read r.running or otherwise classify by phase (P1#1 fix).
//
// r.subscriber.SubscribeEntry orchestrates the full business pipeline:
// business middleware chain → ConsumerBase.Wrap (EntryHandler→SubscriberHandler
// conversion) → observability/principal restore → Inner.Subscribe. The business
// handler stored in handlerConfig.handler is an EntryHandler and flows directly
// into this pipeline without any lifting ceremony.
//
// The recover() path scrubs the panic value through pkg/redaction.RedactString
// and forwards a fixed sentinel error (errSubscriptionPanicked) so a panic value
// containing credentials cannot leak via Health() / Run() / slog "error"
// fields (P1#2 fix). The redacted form is emitted as a typed slog field
// "recovered_redacted" for server-side diagnosis only.
func (r *Router) runSubscribe(ctx context.Context, handlers []handlerConfig, setupErr chan<- subscribeFailure) {
	for _, h := range handlers {
		sub := h.subscription()
		r.wg.Go(func() {
			defer func() {
				if rv := recover(); rv != nil {
					slog.Error("eventrouter: subscription goroutine panicked",
						slog.String("topic", sub.Topic),
						slog.String("cell_id", sub.CellID),
						slog.String("recovered_redacted", redaction.RedactString(fmt.Sprint(rv))))
					setupErr <- subscribeFailure{
						cellID: sub.CellID,
						topic:  sub.Topic,
						reason: SetupErrorReasonPanic,
						err:    errSubscriptionPanicked,
					}
				}
			}()
			slog.Info("eventrouter: starting subscription",
				slog.String("topic", sub.Topic),
				slog.String("consumer_group", sub.ConsumerGroup),
				slog.String("cell_id", sub.CellID))
			err := r.subscriber.SubscribeEntry(ctx, sub, h.handler)
			if err != nil && ctx.Err() == nil {
				setupErr <- subscribeFailure{
					cellID: sub.CellID,
					topic:  sub.Topic,
					reason: SetupErrorReasonSubscribeFailure,
					err:    err,
				}
			}
		})
	}
}

// runAwaitReady waits for Subscriber.Ready(sub) for ALL handlers concurrently
// (Phase 3). Concurrent fan-out avoids goroutine-schedule coupling where a
// serial wait on handler[0] would block if handler[1]'s goroutine happened to
// register first with the in-memory bus.
//
// It also monitors setupErr and ctx cancellation. On error, it cancels the
// context, waits for goroutines, and returns the error.
//
// The Phase 3 consumer is the single owner of setup-phase reason
// classification: it reads the sentinel's reason field
// (SetupErrorReasonSubscribeFailure / SetupErrorReasonPanic) and records the
// setup metric accordingly. ready-timeout records only the setup metric;
// the previous double-write into the runtime metric was a phase-boundary
// violation removed in PR #593 review fix-up (P2#3).
func (r *Router) runAwaitReady(
	ctx context.Context,
	cancel context.CancelFunc,
	handlers []handlerConfig,
	setupErr <-chan subscribeFailure,
) error {
	allReady := r.awaitAllReady(ctx, handlers)

	var deadlineCh <-chan time.Time
	if r.readyTimeout > 0 {
		timer := r.clock.NewTimerAt(r.clock.Now().Add(r.readyTimeout))
		defer timer.Stop()
		deadlineCh = timer.C()
	}

	select {
	case <-allReady:
		// All subscriptions are ready.
		return nil
	case sf := <-setupErr:
		err := fmt.Errorf("eventrouter: topic %s: %w", sf.topic, sf.err)
		r.markHealthError(err)
		slog.Error("eventrouter: subscription error during ready wait, shutting down",
			slog.String("cell_id", sf.cellID),
			slog.String("topic", sf.topic),
			slog.String("reason", sf.reason),
			slog.Any("error", sf.err))
		observability.SafeObserve(slog.Default(), func() {
			r.collector.RecordSetupError(ctx, sf.cellID, sf.topic, sf.reason)
		})
		cancel()
		r.wg.Wait()
		// No drain needed: setupErr buffer == len(handlers) guarantees every
		// runSubscribe goroutine can send once without blocking. ctx cancel
		// stops further sends; remaining buffered errors are GC'd with the
		// channel when Run returns.
		return err
	case <-deadlineCh:
		notReadyHandlers := r.diagnoseNotReadyHandlers(handlers)
		notReady := make([]string, len(notReadyHandlers))
		for i, h := range notReadyHandlers {
			notReady[i] = fmt.Sprintf("%s/%s/%s", h.cellID, h.consumerGroup, h.topic)
			hCopy := h
			observability.SafeObserve(slog.Default(), func() {
				r.collector.RecordSetupError(ctx, hCopy.cellID, hCopy.topic, SetupErrorReasonReadyTimeout)
			})
		}
		err := fmt.Errorf("eventrouter: %d/%d subscriptions not ready after %s: %v",
			len(notReady), len(handlers), r.readyTimeout, notReady)
		r.markHealthError(err)
		slog.Error("eventrouter: ready timeout exceeded, shutting down",
			slog.Duration("timeout", r.readyTimeout),
			slog.Int("not_ready_count", len(notReady)),
			slog.Any("not_ready", notReady))
		cancel()
		r.wg.Wait()
		return err
	case <-ctx.Done():
		r.wg.Wait()
		return ctx.Err()
	}
}

// diagnoseNotReadyHandlers returns handlerConfigs for subscriptions whose Ready
// channel has not closed. It is the typed form of diagnoseNotReady, used both
// for error reporting and for collector RecordSetupError calls.
func (r *Router) diagnoseNotReadyHandlers(handlers []handlerConfig) []handlerConfig {
	var notReady []handlerConfig
	for _, h := range handlers {
		sub := h.subscription()
		select {
		case <-r.subscriber.Ready(sub):
			// ready
		default:
			notReady = append(notReady, h)
		}
	}
	return notReady
}

// awaitAllReady launches one goroutine per handler that waits on Ready, then
// returns a channel that closes when all goroutines complete (or ctx cancels).
// When a Ready signal fires, it records the wait duration and increments the
// active subscription gauge via the EventCollector.
func (r *Router) awaitAllReady(ctx context.Context, handlers []handlerConfig) <-chan struct{} {
	doneCh := make(chan struct{})
	var wg sync.WaitGroup
	for _, h := range handlers {
		sub := h.subscription()
		wg.Go(func() {
			start := r.clock.Now()
			select {
			case <-r.subscriber.Ready(sub):
				elapsed := r.clock.Since(start)
				observability.SafeObserve(slog.Default(), func() {
					r.collector.ObserveReadyWait(ctx, sub.CellID, elapsed)
				})
				observability.SafeObserve(slog.Default(), func() {
					r.collector.IncSubscriptionActive(ctx, sub.CellID)
				})
				r.activeMu.Lock()
				r.activeCells = append(r.activeCells, sub.CellID)
				r.activeMu.Unlock()
			case <-ctx.Done():
				// Handler never readied — do not observe or increment.
			}
		})
	}
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	return doneCh
}

func (h handlerConfig) subscription() outbox.Subscription {
	sub := outbox.Subscription{
		Topic:               h.topic,
		ConsumerGroup:       h.consumerGroup,
		CellID:              h.cellID,
		SliceID:             h.sliceID,
		BrokerDelaySchedule: h.brokerDelaySchedule,
	}
	if h.contract.ID != "" {
		sub.ContractID = h.contract.ID
		sub.ContractKind = string(h.contract.Kind)
		sub.ContractTransport = h.contract.Transport
	}
	return sub
}

// closeRunning safely closes the running channel exactly once.
func (r *Router) closeRunning() {
	r.runningOnce.Do(func() { close(r.running) })
}

// Running returns a channel that is closed when all subscriptions have
// successfully started consuming. Callers can use this to wait for the
// Router to be ready (e.g., in bootstrap).
//
// Note: if Run returns a setup error, Running() is never closed. Callers
// should also monitor the error from Run.
func (r *Router) Running() <-chan struct{} {
	return r.running
}

// Health reports whether the router is ready to serve subscriptions.
// It returns nil only after startup completes successfully and before a setup
// or runtime failure has been recorded. After graceful shutdown it returns a
// distinguishable "shutting down" error.
func (r *Router) Health() error {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	if r.healthErr != nil {
		return r.healthErr
	}
	if r.shutdown {
		return fmt.Errorf("eventrouter: shutting down")
	}
	if !r.started {
		return fmt.Errorf("eventrouter: not running")
	}
	return nil
}

func (r *Router) markRunning() {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.started = true
	r.healthErr = nil
}

func (r *Router) markHealthError(err error) {
	if err == nil {
		return
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.healthErr = err
}

func (r *Router) markShutdown() {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.shutdown = true
}

// Close shuts down the Router in three phases:
//
//  1. StopIntake (optional): if the subscriber implements
//     outbox.SubscriberIntakeStopper, StopIntake(ctx) is called first.
//     This signals the broker to stop delivering new messages (basic.cancel)
//     while in-flight handlers continue to run. Errors from StopIntake are
//     logged as warnings and do not abort the shutdown sequence.
//
//  2. Cancel: the internal runCtx subtree is canceled, unblocking
//     Subscribe goroutines that are waiting on context cancellation.
//
//  3. Drain: wg.Wait() blocks until all goroutines exit. If ctx expires
//     before drain completes, Close returns ctx.Err().
//
// ref: ThreeDotsLabs/watermill message/router.go — closingInProgressCh two-phase barrier.
// ref: uber-go/fx app.go — run ctx vs stop ctx separation.
func (r *Router) Close(ctx context.Context) error {
	start := r.clock.Now()

	// Phase 1: StopIntake — optional graceful degradation.
	// SubscriberWithMiddleware.StopIntake forwards to Inner if Inner implements
	// SubscriberIntakeStopper, or returns nil gracefully otherwise.
	// Subscribers implementing StopIntake stop accepting new deliveries while
	// in-flight handlers continue, enabling a two-phase drain:
	// broker basic.cancel → handler drain → runCtx cancel.
	//
	// F1b: StopIntake runs in a dedicated goroutine and the wait is gated by
	// ctx. A misbehaving adapter that ignores its ctx cannot stall the whole
	// Close chain — once ctx expires we log Warn and advance to Phase 2/3
	// regardless, honoring the caller's shutdown budget.
	{
		stopDone := make(chan error, 1)
		go func() { stopDone <- r.subscriber.StopIntake(ctx) }()
		select {
		case err := <-stopDone:
			if err != nil {
				slog.Warn("eventrouter: StopIntake returned error, continuing shutdown",
					slog.String("phase", "stop_intake"),
					slog.Any("error", err))
			}
		case <-ctx.Done():
			phErr := &phaseError{Phase: "stop_intake", Err: ctx.Err()}
			slog.Warn("eventrouter: StopIntake exceeded ctx budget, advancing to cancel+wait",
				slog.String("phase", phErr.Phase),
				slog.Any("error", phErr.Err),
				slog.Duration("elapsed", r.clock.Since(start)))
			// Do NOT return: we still need to cancel runCtx and reap goroutines
			// so the caller's resources are released.
		}
	}

	// Phase 2: cancel internal runCtx subtree to signal remaining ctx.Done paths.
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Decrement active subscription gauges for every handler that was Inc'd
	// during awaitAllReady. This mirrors the Inc in awaitAllReady exactly.
	r.activeMu.Lock()
	activeCells := make([]string, len(r.activeCells))
	copy(activeCells, r.activeCells)
	r.activeMu.Unlock()
	for _, cellID := range activeCells {
		cid := cellID
		observability.SafeObserve(slog.Default(), func() {
			r.collector.DecSubscriptionActive(ctx, cid)
		})
	}

	// Phase 3: wait for goroutines to drain or ctx expires.
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("eventrouter: closed", slog.Duration("elapsed", r.clock.Since(start)))
		// If ctx already expired (e.g. StopIntake consumed the budget), surface
		// the timeout so callers see consistent shutdown outcomes.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return &phaseError{Phase: "wg_wait", Err: ctxErr}
		}
		return nil
	case <-ctx.Done():
		phErr := &phaseError{Phase: "wg_wait", Err: ctx.Err()}
		slog.Warn("eventrouter: close timed out, some goroutines may still be running",
			slog.String("phase", phErr.Phase),
			slog.Any("error", phErr.Err),
			slog.Duration("elapsed", r.clock.Since(start)))
		return phErr
	}
}

// HandlerCount returns the number of registered handlers.
func (r *Router) HandlerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.handlers)
}
