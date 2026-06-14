package command

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

const (
	msgSweeperNilReceiver    = "command.Sweeper: nil receiver; use NewSweeper to construct"
	msgSweeperNotConstructed = "command.Sweeper must be constructed via NewSweeper"
)

// ExpiryTransition describes a single status change recommended by SweepOnce.
type ExpiryTransition struct {
	CommandID string
	From      Status
	To        Status // typically StatusExpired
	Reason    string // e.g. "phase_overall_deadline"
}

// SweepOnce is a pure function: given a snapshot of non-terminal entries and
// the current time, returns the list of transitions adapters should apply.
// Callers pass entries in ANY non-terminal status (Pending / Sent / Delivered).
// Terminal entries are silently ignored.
//
// Priority: PhaseOverall > PhaseSendToComplete > PhaseScheduleToSend
// (first match wins; returned Reason identifies which phase triggered).
func SweepOnce(entries []Entry, now time.Time) []ExpiryTransition {
	var result []ExpiryTransition
	for _, e := range entries {
		if e.Status.IsTerminal() {
			continue
		}
		t, ok := checkExpiry(&e, now)
		if ok {
			result = append(result, t)
		}
	}
	return result
}

// checkExpiry returns the highest-priority expiry transition for a non-terminal
// entry, or (zero, false) if no phase has expired.
func checkExpiry(e *Entry, now time.Time) (ExpiryTransition, bool) {
	// Priority: PhaseOverall > PhaseSendToComplete > PhaseScheduleToSend
	if dl := e.DeadlineFor(PhaseOverall); !dl.IsZero() && now.After(dl) {
		return ExpiryTransition{
			CommandID: e.ID,
			From:      e.Status,
			To:        StatusExpired,
			Reason:    "phase_overall_deadline",
		}, true
	}
	if dl := e.DeadlineFor(PhaseSendToComplete); !dl.IsZero() && now.After(dl) {
		return ExpiryTransition{
			CommandID: e.ID,
			From:      e.Status,
			To:        StatusExpired,
			Reason:    "phase_send_to_complete",
		}, true
	}
	if dl := e.DeadlineFor(PhaseScheduleToSend); !dl.IsZero() && now.After(dl) {
		return ExpiryTransition{
			CommandID: e.ID,
			From:      e.Status,
			To:        StatusExpired,
			Reason:    "phase_schedule_to_send",
		}, true
	}
	return ExpiryTransition{}, false
}

// Sweeper is a kernel-level command expiry actor. It implements
// reconcile.Reconciler: each Reconcile call scans non-terminal commands (via
// ActiveScanner) and terminates expired entries (via Queue.Ack(AckTimeout)).
// It is the reconcile runtime's first real consumer — a reconcile.Loop drives
// it on a TickerTrigger cadence (the per-tick control shell that used to live in
// runtime/command has been deleted).
//
// Time source: the Sweeper holds the cell's business clock (clk). Reconcile —
// whose signature carries no "now" — sources the sweep time from clk.Now(),
// following the reconcile convention that a Reconciler owns its own domain
// clock. Control-plane scheduling (the tick cadence, startup probe, requeue
// timers) is NOT the Sweeper's concern: it is owned by reconcile.Loop's sealed
// real-only clock and the injected TickerTrigger clock. The lower-level
// SweepTick(ctx, now) primitive still takes an explicit "now", keeping
// business-plane sweep logic deterministically testable without clock injection.
//
// All non-built fields are unexported — callers cannot populate them via
// `&command.Sweeper{scanner: ...}` literals. The remaining attack surface is
// the bare zero-value literal `&command.Sweeper{}`, which produces an instance
// with nil scanner / queue / clk and would panic on the first sweep. The
// unexported `built` sentinel + head fail-closed (SweepTick and Reconcile) turns
// that panic into a clean error: only NewSweeper sets `built=true`, so any
// literal-zero Sweeper short-circuits before dereferencing scanner / clk.
//
// Filter narrows the scan; zero value (default) means "all devices, all
// non-terminal statuses". Adapters decide whether ScanFilter is honored
// efficiently (e.g., indexed by device_id) or scanned in memory.
//
// ref: Temporal HistoryService timer scan loop — role-based periodic scan
// over active timers; disposition (expire vs retry) is a separate decision.
// ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go (Reconciler).
// ref: kernel/outbox.ConsumerBase.built — same sentinel pattern.
type Sweeper struct {
	scanner ActiveScanner
	queue   Queue
	filter  ScanFilter
	clk     clock.Clock // business clock; Reconcile sources "now" from clk.Now()

	// built is the construction sentinel; only NewSweeper sets it to true.
	// SweepTick() / Reconcile() reject any Sweeper with built==false, closing the
	// `&command.Sweeper{}` zero-value literal attack surface.
	built bool
}

// SweeperOption configures optional Sweeper fields. Pass into NewSweeper as
// variadic args. The required positional dependencies (scanner, queue) are
// NOT exposed as options so the type system enforces their presence.
type SweeperOption func(*Sweeper)

// WithSweeperFilter narrows the scan to a specific device or status set.
// Default: zero-value ScanFilter (all devices, all non-terminal statuses).
func WithSweeperFilter(f ScanFilter) SweeperOption {
	return func(s *Sweeper) { s.filter = f }
}

// NewSweeper constructs a Sweeper. scanner and queue are required interface
// dependencies; nil triggers fail-fast per validation.IsNilInterface (typed-nil
// safety, ≈ OUTBOX-SERVICE-01 pattern). clk is the mandatory business clock —
// Reconcile sources the sweep "now" from clk.Now(); a nil clock is a programmer
// error and panics (clock.MustHaveClock) per CLOCK-POSITIONAL-INJECTION-01.
//
// Example:
//
//	sweeper, err := command.NewSweeper(scanner, queue, clk,
//	    command.WithSweeperFilter(command.ScanFilter{DeviceID: "dev-1"}),
//	)
//	if err != nil {
//	    return fmt.Errorf("sweeper: %w", err)
//	}
func NewSweeper(scanner ActiveScanner, queue Queue, clk clock.Clock, opts ...SweeperOption) (*Sweeper, error) {
	if validation.IsNilInterface(scanner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: scanner required")
	}
	if validation.IsNilInterface(queue) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: queue required")
	}
	clock.MustHaveClock(clk, "command: NewSweeper: clk required")
	s := &Sweeper{
		scanner: scanner,
		queue:   queue,
		clk:     clk,
		built:   true,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Validate reports whether the Sweeper is ready to run, with NO side effects
// (no scan, no Ack). It is the readiness gate reconcile.Loop invokes before
// Start (the reconcilerReadinessChecker seam) so a misconstructed sweeper fails
// startup (bootstrap rolls back) instead of starting and erroring on every tick
// (review P2-1).
//
// It catches exactly the cases SweepTick's head guards catch — nil receiver
// and the zero-value &command.Sweeper{} literal (built==false) — plus a
// defensive nil scanner/queue check (NewSweeper already guarantees these when
// built, but Validate is the single readiness contract so it states the full
// invariant).
func (s *Sweeper) Validate() error {
	if s == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgSweeperNilReceiver)
	}
	if !s.built {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgSweeperNotConstructed)
	}
	if validation.IsNilInterface(s.scanner) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command.Sweeper: scanner is nil")
	}
	if validation.IsNilInterface(s.queue) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command.Sweeper: queue is nil")
	}
	return nil
}

// SweepTick executes a single sweep: read non-terminal entries via
// ScanActive, compute expirations via SweepOnce, terminate each expired entry
// via Queue.Ack(AckTimeout).
//
// Error semantics:
//   - ScanActive error: immediately short-circuits the tick and returns the
//     scan error. No Ack calls are made (partial-tick risk is avoided).
//   - Ack errors (scan succeeded): all Ack errors from the full transition set
//     are aggregated via errors.Join and returned. A single Ack failure does
//     not abort remaining Ack calls.
//
// The caller (control plane) is responsible for logging and metrics.
//
// Two head guards close the literal-construction attack surface:
//
//  1. `if s == nil` — `var s *Sweeper; s.SweepTick(ctx, now)` would otherwise
//     panic on the s.built read below; the guard returns a typed error.
//  2. `if !s.built` — closes the zero-value `&command.Sweeper{}` literal bypass
//     (only NewSweeper sets built=true).
func (s *Sweeper) SweepTick(ctx context.Context, now time.Time) error {
	if s == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgSweeperNilReceiver)
	}
	if !s.built {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgSweeperNotConstructed)
	}

	entries, scanErr := s.scanner.ScanActive(ctx, s.filter)
	if scanErr != nil {
		return scanErr
	}

	transitions := SweepOnce(entries, now)
	var errs []error
	for _, t := range transitions {
		if err := s.queue.Ack(ctx, t.CommandID, AckTimeout, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Reconcile implements reconcile.Reconciler. The Sweeper is a bulk scan-all
// actor: req.EntityID is ignored. A TickerTrigger emits Request{} (empty
// EntityID) as the "resync-all" pulse, and the Sweeper has no per-entity path —
// every call performs one full SweepTick at clk.Now().
//
// Error semantics: any sweep error (scan failure or aggregated Ack failures) is
// returned as-is. The reconcile.Loop classifies a bare error as transient and
// backs off + retries on the next tick; nothing the Sweeper produces is a
// reconcile.PermanentError (a transient DB/broker fault is always retryable).
// On success Reconcile returns the zero Result{} (RequeueAfter == 0), so the
// Loop re-observes at its configured tick interval — the TickerTrigger cadence
// is the real driver, making the sweep level-triggered.
//
// The head guard mirrors SweepTick's: a nil receiver or zero-value
// &command.Sweeper{} (built==false) fails closed with an error BEFORE
// dereferencing s.clk, instead of panicking. A Sweeper wired through
// reconcile.New(...).Build() can never be in that state (Build rejects a
// typed-nil reconciler), so this is defense-in-depth.
func (s *Sweeper) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	if s == nil {
		return reconcile.Result{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgSweeperNilReceiver)
	}
	if !s.built {
		return reconcile.Result{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgSweeperNotConstructed)
	}
	if err := s.SweepTick(ctx, s.clk.Now()); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}
