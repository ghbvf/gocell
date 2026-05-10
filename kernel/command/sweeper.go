package command

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
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
// All fields are unexported — callers MUST construct via NewSweeper, which
// fail-fasts on missing required deps. AI-HARD per ai-collab.md
// §"违反不可表达": &command.Sweeper{...} literal construction is impossible
// from outside the kernel/command package, so a developer cannot accidentally
// omit the required Clock dependency.
//
// Filter narrows the scan; zero value (default) means "all devices, all
// non-terminal statuses". Adapters decide whether ScanFilter is honored
// efficiently (e.g., indexed by device_id) or scanned in memory.
//
// ref: Temporal HistoryService timer scan loop — role-based periodic scan
// over active timers; disposition (expire vs retry) is a separate decision.
type Sweeper struct {
	scanner  ActiveScanner
	queue    Queue
	filter   ScanFilter
	interval time.Duration
	clk      clock.Clock
	onError  func(error)
}

// SweeperOption configures optional Sweeper fields. Pass into NewSweeper as
// variadic args. The required positional dependencies (scanner, queue, clk)
// are NOT exposed as options so the type system enforces their presence.
type SweeperOption func(*Sweeper)

// WithSweeperFilter narrows the scan to a specific device or status set.
// Default: zero-value ScanFilter (all devices, all non-terminal statuses).
func WithSweeperFilter(f ScanFilter) SweeperOption {
	return func(s *Sweeper) { s.filter = f }
}

// WithSweeperInterval sets the tick interval. Default 30s when not set or
// when set to a non-positive value.
func WithSweeperInterval(d time.Duration) SweeperOption {
	return func(s *Sweeper) { s.interval = d }
}

// WithSweeperOnError registers a non-fatal error callback. nil is permitted
// (semantically equivalent to no callback).
func WithSweeperOnError(fn func(error)) SweeperOption {
	return func(s *Sweeper) { s.onError = fn }
}

// NewSweeper constructs a Sweeper. The three positional parameters are all
// required dependencies; nil triggers fail-fast per
// runtime-api.md §"强依赖 wiring option" pattern (≈ OUTBOX-SERVICE-01).
//
// Example:
//
//	sweeper, err := command.NewSweeper(scanner, queue, clock.Real(),
//	    command.WithSweeperInterval(30*time.Second),
//	    command.WithSweeperOnError(func(err error) { logger.Error("sweeper", err) }),
//	)
//	if err != nil {
//	    return fmt.Errorf("sweeper: %w", err)
//	}
func NewSweeper(scanner ActiveScanner, queue Queue, clk clock.Clock, opts ...SweeperOption) (*Sweeper, error) {
	if scanner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: scanner required")
	}
	if queue == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: queue required")
	}
	if clk == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: NewSweeper: clock required")
	}
	s := &Sweeper{
		scanner:  scanner,
		queue:    queue,
		clk:      clk,
		interval: defaultCommandSweeperInterval,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Start begins the sweep loop, blocking until ctx is canceled. Required
// dependencies are validated at NewSweeper construction time, so Start does
// not re-check them.
func (s *Sweeper) Start(ctx context.Context) error {
	interval := s.interval
	if interval <= 0 {
		interval = defaultCommandSweeperInterval
	}

	ticker := s.clk.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			s.runTick(ctx, s.clk.Now())
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
	entries, err := s.scanner.ScanActive(ctx, s.filter)
	if err != nil {
		if s.onError != nil {
			s.onError(err)
		}
		return
	}

	transitions := SweepOnce(entries, now)
	for _, t := range transitions {
		if err := s.queue.Ack(ctx, t.CommandID, AckTimeout, now); err != nil {
			if s.onError != nil {
				s.onError(err)
			}
		}
	}
}
