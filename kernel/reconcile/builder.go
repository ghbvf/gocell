package reconcile

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/validation"
)

// Builder is the sole public construction entry point for a Loop.
// Use New(reconciler).With*(...).Build() to create a Loop.
//
// Design: Builder fields are all unexported; Build() constructs a *Loop with
// private fields set from the Builder's configuration and returns it.
// A Loop with no configured Trigger never receives external work and cannot
// converge — Build() returns an error in that case.
//
// Missing-required validation in Build:
//   - reconciler must be non-nil (validation.IsNilInterface guard).
//   - trigger must be provided (missing trigger → Loop never gets work).
//   - a FencedRepo requires a Leader: fencing is only meaningful with a
//     leadership epoch source; WithFencedRepo without WithLeader would silently
//     bind Epoch 0 (no fencing), so Build rejects it.
//
// All other runtime fail-fasts (typed-nil Leader/FencedRepo, backoff inversion,
// reconcilerID charset) are preserved in preStartValidate (Loop.Start) —
// not duplicated in Build.
//
// There is no WithClock option by design: control-plane scheduling (timers,
// startup probe, lease renew) uses a sealed real-only clock (see
// Loop.controlPlaneClock); Reconciler implementations source their own domain
// clock.
//
// There is no WithLogger option by design either: the Loop logs through
// slog.Default(), which every production entry point seals to the redacting
// process-global sink (runtime/observability/logging). A per-loop raw logger
// would bypass that fail-closed redaction. (So the With* set is the faithful
// superset of every CONSUMER-configurable field — logger and the control-plane
// clock are deliberately framework-owned, not Builder inputs.)
//
// ref: kubernetes-sigs/controller-runtime pkg/builder/controller.go
// (TypedBuilder.Build — fields unexported, fluent With* return *Builder,
// terminal Build validates and constructs the controller).
type Builder struct {
	reconciler              Reconciler
	trigger                 Trigger
	leader                  LeaderElector
	fencedRepo              FencedRepository
	metrics                 Metrics
	name                    string
	reconcilerID            string
	interval                time.Duration
	maxConcurrentReconciles int
	baseDelay               time.Duration
	maxDelay                time.Duration
	renewInterval           time.Duration
}

// New creates a new Builder with the required Reconciler. The Reconciler is
// validated for typed-nil in Build().
//
// ref: controller-runtime ControllerManagedBy — GoCell uses reconciler-first
// entry (no manager concept) per ADR §3.5.
func New(reconciler Reconciler) *Builder {
	return &Builder{reconciler: reconciler}
}

// WithTrigger sets the Trigger that feeds Requests into the Loop's queue.
// Trigger is required; Build() returns an error if it is not set.
func (b *Builder) WithTrigger(trigger Trigger) *Builder {
	b.trigger = trigger
	return b
}

// WithLeader sets an optional LeaderElector. When nil (default), the Loop
// runs in single-process mode (always leader, Epoch 0, no fencing).
func (b *Builder) WithLeader(leader LeaderElector) *Builder {
	b.leader = leader
	return b
}

// WithFencedRepo sets the optional epoch-aware write seam for cross-replica
// correctness. When nil (default), no FencedWriter is injected into Reconcile.
func (b *Builder) WithFencedRepo(repo FencedRepository) *Builder {
	b.fencedRepo = repo
	return b
}

// WithConcurrency sets the maximum number of concurrent Reconcile calls across
// distinct EntityIDs. Same-EntityID reconciles are always serial. Defaults to 1.
func (b *Builder) WithConcurrency(n int) *Builder {
	b.maxConcurrentReconciles = n
	return b
}

// WithBackoff sets the initial (base) and maximum backoff delays for
// transient errors. Both default to library constants if not set.
func (b *Builder) WithBackoff(base, max time.Duration) *Builder {
	b.baseDelay = base
	b.maxDelay = max
	return b
}

// WithMetrics sets pre-registered metric instruments.
func (b *Builder) WithMetrics(m Metrics) *Builder {
	b.metrics = m
	return b
}

// WithInterval sets the requeue delay for Result{} (RequeueAfter == 0).
// Defaults to 30s.
func (b *Builder) WithInterval(d time.Duration) *Builder {
	b.interval = d
	return b
}

// WithName sets the loop name used in log fields. Defaults to "reconcile.loop".
func (b *Builder) WithName(name string) *Builder {
	b.name = name
	return b
}

// WithReconcilerID sets the metric/log owner dimension. Must be a
// low-cardinality, label-safe identifier (validated by Loop.Start). Defaults
// to the "_runtime" sentinel.
func (b *Builder) WithReconcilerID(id string) *Builder {
	b.reconcilerID = id
	return b
}

// WithRenewInterval sets the lease renew cadence override (Leader != nil).
// Zero → derived as (ExpiresAt-AcquiredAt)/3 from the acquired token.
func (b *Builder) WithRenewInterval(d time.Duration) *Builder {
	b.renewInterval = d
	return b
}

// Build validates the Builder configuration and constructs a *Loop.
//
// Required fields:
//   - Reconciler (set via New): must be non-nil (validation.IsNilInterface guard).
//   - Trigger (set via WithTrigger): must be provided; a Loop with no Trigger
//     never receives external work and cannot converge toward desired state.
//   - FencedRepo requires a Leader: fencing needs a leadership epoch source, so
//     WithFencedRepo without WithLeader is rejected (it would silently bind
//     Epoch 0 = no fencing).
//
// All other validations (reconcilerID charset, backoff ordering, typed-nil
// Leader/FencedRepo) run at Loop.Start via preStartValidate.
//
// On success, Build creates the internal triggerCh (buffer = queueBuffer),
// wires trigger into loop.trigger, and sets loop.source to the read end of
// triggerCh. No goroutines are started; Start() begins execution.
func (b *Builder) Build() (*Loop, error) {
	if validation.IsNilInterface(b.reconciler) {
		return nil, fmt.Errorf("reconcile: Builder requires a non-nil Reconciler; pass it to reconcile.New(reconciler)")
	}
	if validation.IsNilInterface(b.trigger) {
		return nil, fmt.Errorf(
			"reconcile: Builder requires a Trigger (a Loop with no trigger never receives work); " +
				"use WithTrigger")
	}
	if !validation.IsNilInterface(b.fencedRepo) && validation.IsNilInterface(b.leader) {
		return nil, fmt.Errorf(
			"reconcile: WithFencedRepo requires WithLeader (fencing needs a leadership epoch source; " +
				"without a Leader the writer binds Epoch 0 = no fencing)")
	}

	triggerCh := make(chan Request, queueBuffer)
	return &Loop{
		reconciler:              b.reconciler,
		trigger:                 b.trigger,
		triggerCh:               triggerCh,
		source:                  triggerCh,
		leader:                  b.leader,
		fencedRepo:              b.fencedRepo,
		metrics:                 b.metrics,
		name:                    b.name,
		reconcilerID:            b.reconcilerID,
		interval:                b.interval,
		maxConcurrentReconciles: b.maxConcurrentReconciles,
		baseDelay:               b.baseDelay,
		maxDelay:                b.maxDelay,
		renewInterval:           b.renewInterval,
	}, nil
}
