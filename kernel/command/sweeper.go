package command

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
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

// Sweeper is a kernel-level command expiry actor that, on each SweepTick call,
// scans non-terminal commands (via ActiveScanner) and terminates expired entries
// (via Queue.Ack(AckTimeout)).
//
// Sweeper has no clock or ticker fields — time source and tick scheduling are
// entirely owned by the control plane (runtime/command.SweeperLifecycle). This
// makes "inject a fake clock into Sweeper for control-plane timing" impossible
// at the type level (C.1 Hard carrier): there is no clock field to inject.
//
// AI-rebust 评级：**C.1 Hard（类型不可表达）** — Sweeper 无任何时钟字段；
// 控制面 fake clock 在 kernel 类型层不可表达。runtime 层控制面真实时间
// carve-out 是 Medium（archtest 函数级白名单），Hard 升级路径点名
// backlog ID CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01。
//
// Business-plane time (now) is passed explicitly to SweepTick and SweepOnce,
// preserving full determinism for business logic tests without requiring any
// clock injection into Sweeper itself.
//
// All non-built fields are unexported — callers cannot populate them via
// `&command.Sweeper{Scanner: ...}` literals. The remaining attack surface is
// the bare zero-value literal `&command.Sweeper{}`, which produces an instance
// with nil scanner / queue and would panic on the first SweepTick call.
// The unexported `built` sentinel + SweepTick head fail-closed turns that panic
// into a clean error: only NewSweeper sets `built=true`, so any literal-zero
// Sweeper short-circuits at SweepTick.
//
// Filter narrows the scan; zero value (default) means "all devices, all
// non-terminal statuses". Adapters decide whether ScanFilter is honored
// efficiently (e.g., indexed by device_id) or scanned in memory.
//
// ref: Temporal HistoryService timer scan loop — role-based periodic scan
// over active timers; disposition (expire vs retry) is a separate decision.
// ref: kernel/outbox.ConsumerBase.built — same sentinel pattern.
type Sweeper struct {
	scanner ActiveScanner
	queue   Queue
	filter  ScanFilter

	// built is the construction sentinel; only NewSweeper sets it to true.
	// SweepTick() rejects any Sweeper with built==false, closing the
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

// NewSweeper constructs a Sweeper. The two positional parameters are required
// dependencies; nil triggers fail-fast per validation.IsNilInterface
// (typed-nil safety, ≈ OUTBOX-SERVICE-01 pattern).
//
// Clock is intentionally absent from the constructor: control-plane timing
// (ticker interval, startup probe) is owned entirely by runtime/command.
// SweeperLifecycle. Business-plane time is passed per-tick via SweepTick(now).
//
// Example:
//
//	sweeper, err := command.NewSweeper(scanner, queue,
//	    command.WithSweeperFilter(command.ScanFilter{DeviceID: "dev-1"}),
//	)
//	if err != nil {
//	    return fmt.Errorf("sweeper: %w", err)
//	}
func NewSweeper(scanner ActiveScanner, queue Queue, opts ...SweeperOption) (*Sweeper, error) {
	if validation.IsNilInterface(scanner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: scanner required")
	}
	if validation.IsNilInterface(queue) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: queue required")
	}
	s := &Sweeper{
		scanner: scanner,
		queue:   queue,
		built:   true,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Validate reports whether the Sweeper is ready to run, with NO side effects
// (no scan, no Ack). It is the readiness gate runtime/command.SweeperLifecycle
// invokes at OnStart so a misconstructed sweeper fails startup (bootstrap
// rolls back) instead of starting and erroring on every tick (review P2-1).
//
// It catches exactly the cases SweepTick's head guards catch — nil receiver
// and the zero-value &command.Sweeper{} literal (built==false) — plus a
// defensive nil scanner/queue check (NewSweeper already guarantees these when
// built, but Validate is the single readiness contract so it states the full
// invariant).
func (s *Sweeper) Validate() error {
	if s == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command.Sweeper: nil receiver; use NewSweeper to construct")
	}
	if !s.built {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command.Sweeper must be constructed via NewSweeper")
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
			"command.Sweeper: nil receiver; use NewSweeper to construct")
	}
	if !s.built {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command.Sweeper must be constructed via NewSweeper")
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
