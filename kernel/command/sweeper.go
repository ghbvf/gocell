package command

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
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

// Sweeper is a kernel-level background worker that periodically calls
// Reader.PendingCommands (per-device or globally via an adapter-specific
// query) and applies SweepOnce via StateAdvancer.
//
// Implements kernel/worker.Worker (Start blocks until ctx cancelled; Stop idempotent).
//
// NOTE: Sweeper does NOT call the idempotency Claimer; sweep semantics are
// pure read-and-advance. Adapters MUST use optimistic locking in AdvanceStatus
// to prevent double-expire across replicas.
type Sweeper struct {
	// Reader is required. PendingCommands is called on each tick.
	Reader Reader
	// Advancer is required. AdvanceStatus is called for each expired entry.
	Advancer StateAdvancer
	// Interval is how often to scan; defaults to 30s if zero.
	Interval time.Duration
	// Now supplies the clock; defaults to time.Now if nil.
	Now func() time.Time
	// DeviceID, if non-empty, restricts the sweep to one device (for adapters
	// without a global scan API). Must be non-empty — Sweeper.Start returns
	// an error if DeviceID is empty to prevent inadvertent scope creep into
	// global-sweep adapter concerns.
	//
	// Future extension for global sweep (all devices): introduce a
	// Reader.PendingAll method and accept DeviceID="" as the global-sweep
	// sentinel, OR provide a separate GlobalSweeper type. Current per-device
	// restriction keeps adapter concerns decoupled — the postgres adapter
	// (Wave 3) will likely add PendingAll and relax this check.
	DeviceID string
	// OnError is invoked on non-fatal errors during a sweep tick. nil = no-op.
	OnError func(error)
}

// Start begins the sweep loop, blocking until ctx is cancelled.
// Returns an error if DeviceID is empty (required for scoped sweeps).
func (s *Sweeper) Start(ctx context.Context) error {
	if s.DeviceID == "" {
		return errDeviceIDRequired
	}

	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	nowFn := s.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.runTick(ctx, nowFn())
		}
	}
}

// Stop is idempotent. Since Start exits on ctx cancellation, Stop is a no-op
// that callers can call safely multiple times.
func (s *Sweeper) Stop(_ context.Context) error {
	return nil
}

// runTick executes a single sweep: read pending entries, compute transitions,
// apply them. Non-fatal errors are forwarded to OnError.
func (s *Sweeper) runTick(ctx context.Context, now time.Time) {
	entries, err := s.Reader.PendingCommands(ctx, s.DeviceID)
	if err != nil {
		if s.OnError != nil {
			s.OnError(err)
		}
		return
	}

	transitions := SweepOnce(entries, now)
	for _, t := range transitions {
		if err := s.Advancer.AdvanceStatus(ctx, t.CommandID, t.From, t.To, now); err != nil {
			if s.OnError != nil {
				s.OnError(err)
			}
		}
	}
}

// errDeviceIDRequired is returned by Sweeper.Start when DeviceID is empty.
// Callers can match with errors.Is against errcode.ErrValidationFailed.
var errDeviceIDRequired = errcode.New(errcode.ErrValidationFailed, "command: Sweeper.DeviceID must be non-empty")
