package command

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	// defaultCommandSweeperInterval is the default sweep tick interval when
	// none is specified in the Sweeper configuration.
	defaultCommandSweeperInterval = 30 * time.Second
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

// Sweeper is a kernel-level background worker that periodically scans
// non-terminal commands (via ActiveScanner) and terminates expired entries
// (via Queue.Ack(AckTimeout)).
//
// Implements kernel/worker.Worker (Start blocks until ctx canceled; Stop idempotent).
//
// By default Sweeper scans all devices (Filter.DeviceID=""). Scoping to a
// single device is a filter option, not a structural field — adapters decide
// whether the ScanFilter is honored efficiently (e.g., indexed by device_id)
// or scanned in memory.
//
// ref: Temporal HistoryService timer scan loop — role-based periodic scan
// over active timers; disposition (expire vs retry) is a separate decision.
type Sweeper struct {
	// Scanner is required. ScanActive is called on each tick with Filter.
	Scanner ActiveScanner
	// Queue is required. Ack(AckTimeout) finalizes expired entries to StatusExpired.
	Queue Queue
	// Filter narrows the scan; zero value means "all devices, all non-terminal statuses".
	Filter ScanFilter
	// Interval is how often to scan; defaults to 30s if zero.
	Interval time.Duration
	// Now supplies the clock; defaults to time.Now if nil.
	Now func() time.Time
	// OnError is invoked on non-fatal errors during a sweep tick. nil = no-op.
	OnError func(error)
}

// Start begins the sweep loop, blocking until ctx is canceled.
func (s *Sweeper) Start(ctx context.Context) error {
	if s.Scanner == nil {
		return errcode.New(errcode.ErrValidationFailed, "command: Sweeper.Scanner must be non-nil")
	}
	if s.Queue == nil {
		return errcode.New(errcode.ErrValidationFailed, "command: Sweeper.Queue must be non-nil")
	}

	interval := s.Interval
	if interval <= 0 {
		interval = defaultCommandSweeperInterval
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

// runTick executes a single sweep: read non-terminal entries, compute
// expirations, terminate each via Queue.Ack(AckTimeout). Non-fatal errors
// are forwarded to OnError.
func (s *Sweeper) runTick(ctx context.Context, now time.Time) {
	entries, err := s.Scanner.ScanActive(ctx, s.Filter)
	if err != nil {
		if s.OnError != nil {
			s.OnError(err)
		}
		return
	}

	transitions := SweepOnce(entries, now)
	for _, t := range transitions {
		if err := s.Queue.Ack(ctx, t.CommandID, AckTimeout, now); err != nil {
			if s.OnError != nil {
				s.OnError(err)
			}
		}
	}
}
