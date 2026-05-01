package command_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// testNoTimeoutHours is used to verify that NoTimeouts entries are not
// expired even when now is far in the future (10 hours past creation).
const testNoTimeoutHours = 10 * time.Hour

// ---------------------------------------------------------------------------
// SweepOnce tests
// ---------------------------------------------------------------------------

func TestSweepOnce_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	result := command.SweepOnce(nil, time.Now())
	assert.Nil(t, result)

	result = command.SweepOnce([]command.Entry{}, time.Now())
	assert.Nil(t, result)
}

func TestSweepOnce_TerminalIgnored(t *testing.T) {
	t.Parallel()
	terminals := []command.Status{
		command.StatusSucceeded,
		command.StatusFailed,
		command.StatusExpired,
		command.StatusCanceled,
	}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, s := range terminals {
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
				OverallDeadline: testtime.D1s,
			}, created)
			// Force terminal status
			e.Status = s

			// now is well past any deadline
			now := created.Add(testtime.D10min)
			result := command.SweepOnce([]command.Entry{e}, now)
			assert.Nil(t, result, "terminal entry %s must be ignored", s)
		})
	}
}

func TestSweepOnce_NoTimeoutsConfigured_NoTransitions(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{}, created)
	// No timeouts configured — no transitions expected regardless of time.
	now := created.Add(testNoTimeoutHours)
	result := command.SweepOnce([]command.Entry{e}, now)
	assert.Nil(t, result)
}

func TestSweepOnce_ScheduleToSendExpired(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		ScheduleToSend: testtime.D30s,
	}, created)
	// now is past ScheduleToSend deadline
	now := created.Add(testtime.D60s)
	result := command.SweepOnce([]command.Entry{e}, now)
	require.Len(t, result, 1)
	assert.Equal(t, "cmd-1", result[0].CommandID)
	assert.Equal(t, command.StatusPending, result[0].From)
	assert.Equal(t, command.StatusExpired, result[0].To)
	assert.Equal(t, "phase_schedule_to_send", result[0].Reason)
}

func TestSweepOnce_SendToCompleteExpired(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		SendToComplete: testtime.D5min,
	}, created)
	// Advance to Sent
	sentAt := created.Add(testtime.D10s)
	require.NoError(t, command.AdvanceCommand(&e, command.StatusSent, sentAt))

	// now is past SendToComplete deadline (sentAt + 5 min)
	now := sentAt.Add(testtime.D10min)
	result := command.SweepOnce([]command.Entry{e}, now)
	require.Len(t, result, 1)
	assert.Equal(t, "cmd-1", result[0].CommandID)
	assert.Equal(t, command.StatusSent, result[0].From)
	assert.Equal(t, command.StatusExpired, result[0].To)
	assert.Equal(t, "phase_send_to_complete", result[0].Reason)
}

func TestSweepOnce_OverallExpired(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1h,
	}, created)
	// now is past OverallDeadline
	now := created.Add(testtime.D2h)
	result := command.SweepOnce([]command.Entry{e}, now)
	require.Len(t, result, 1)
	assert.Equal(t, "phase_overall_deadline", result[0].Reason)
	assert.Equal(t, command.StatusExpired, result[0].To)
}

func TestSweepOnce_PriorityOverallWinsAgainstPhase(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Both ScheduleToSend AND OverallDeadline are exceeded.
	// PhaseOverall has higher priority and should win.
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		ScheduleToSend:  testtime.D30s,
		OverallDeadline: testtime.D1min,
	}, created)
	// now exceeds both
	now := created.Add(testtime.D5min)
	result := command.SweepOnce([]command.Entry{e}, now)
	require.Len(t, result, 1)
	// Overall wins
	assert.Equal(t, "phase_overall_deadline", result[0].Reason)
}

func TestSweepOnce_NotYetExpired(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1h,
	}, created)
	// now is before deadline
	now := created.Add(testtime.D30min)
	result := command.SweepOnce([]command.Entry{e}, now)
	assert.Nil(t, result)
}

func TestSweepOnce_SendToCompleteIgnoredForPending(t *testing.T) {
	t.Parallel()
	// Pending entry cannot trigger SendToComplete because SentAt is nil.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		SendToComplete: testtime.D5min,
	}, created)
	// e.Status == Pending, SentAt == nil
	now := created.Add(testtime.D10min)
	result := command.SweepOnce([]command.Entry{e}, now)
	assert.Nil(t, result, "SendToComplete cannot trigger for Pending entry (SentAt is nil)")
}

func TestSweepOnce_CoversSentAndDelivered(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Build a Sent entry and a Delivered entry, both past OverallDeadline.
	sentEntry := command.NewEntry("cmd-sent", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	sentAt := created.Add(testtime.D10s)
	require.NoError(t, command.AdvanceCommand(&sentEntry, command.StatusSent, sentAt))

	delivEntry := command.NewEntry("cmd-deliv", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	require.NoError(t, command.AdvanceCommand(&delivEntry, command.StatusSent, sentAt))
	require.NoError(t, command.AdvanceCommand(&delivEntry, command.StatusDelivered, sentAt.Add(time.Second)))

	now := created.Add(testtime.D5min) // past OverallDeadline for both
	result := command.SweepOnce([]command.Entry{sentEntry, delivEntry}, now)
	require.Len(t, result, 2, "Sweeper must expire both Sent and Delivered entries past deadline")
	byID := map[string]command.ExpiryTransition{
		result[0].CommandID: result[0],
		result[1].CommandID: result[1],
	}
	assert.Equal(t, command.StatusSent, byID["cmd-sent"].From)
	assert.Equal(t, command.StatusDelivered, byID["cmd-deliv"].From)
	assert.Equal(t, command.StatusExpired, byID["cmd-sent"].To)
	assert.Equal(t, command.StatusExpired, byID["cmd-deliv"].To)
	assert.Equal(t, "phase_overall_deadline", byID["cmd-sent"].Reason)
	assert.Equal(t, "phase_overall_deadline", byID["cmd-deliv"].Reason)
}

// ---------------------------------------------------------------------------
// Sweeper integration tests (new: uses ActiveScanner + Queue)
// ---------------------------------------------------------------------------

// mockScanner is an in-test ActiveScanner that returns a fixed set of entries.
type mockScanner struct {
	mu         sync.Mutex
	entries    []command.Entry
	err        error
	calls      int
	lastFilter command.ScanFilter
}

func (r *mockScanner) ScanActive(_ context.Context, filter command.ScanFilter) ([]command.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastFilter = filter
	return append([]command.Entry(nil), r.entries...), r.err
}

func (r *mockScanner) GetCommand(_ context.Context, _ string) (*command.Entry, error) {
	return &command.Entry{}, nil
}

// mockAckQueue records Queue.Ack calls; other Queue methods are unused in
// sweeper tests and return errors to catch accidental use.
type mockAckQueue struct {
	mu    sync.Mutex
	calls []ackCall
	err   error
}

type ackCall struct {
	id     string
	reason command.AckReason
}

func (a *mockAckQueue) Ack(_ context.Context, id string, reason command.AckReason, _ time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, ackCall{id: id, reason: reason})
	return a.err
}

// unused methods.
func (a *mockAckQueue) Enqueue(context.Context, command.Entry, command.EnqueueOptions) error {
	return errors.New("unused")
}
func (a *mockAckQueue) Dequeue(context.Context, string, int, time.Duration) ([]command.Entry, error) {
	return nil, errors.New("unused")
}
func (a *mockAckQueue) Report(context.Context, string, time.Time) error { return errors.New("unused") }
func (a *mockAckQueue) ExtendLease(context.Context, string, time.Duration, time.Time) error {
	return errors.New("unused")
}
func (a *mockAckQueue) Cancel(context.Context, string, time.Time) error {
	return errors.New("unused")
}

func (a *mockAckQueue) CallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *mockAckQueue) Calls() []ackCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ackCall(nil), a.calls...)
}

var _ command.Queue = (*mockAckQueue)(nil)

func TestSweeper_Start_CtxCancelExits(t *testing.T) {
	defer goleak.VerifyNone(t)
	scanner := &mockScanner{}
	q := &mockAckQueue{}
	s := &command.Sweeper{
		Scanner:  scanner,
		Queue:    q,
		Interval: testtime.D10ms,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.D2s):
		t.Fatal("Start did not exit after ctx cancel")
	}
}

func TestSweeper_Stop_Idempotent(t *testing.T) {
	t.Parallel()
	s := &command.Sweeper{
		Scanner: &mockScanner{},
		Queue:   &mockAckQueue{},
	}
	// Stop is a no-op regardless of state.
	assert.NoError(t, s.Stop(context.Background()))
	assert.NoError(t, s.Stop(context.Background()))
}

func TestSweeper_Start_InvokesQueueAckOnExpired(t *testing.T) {
	defer goleak.VerifyNone(t)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredEntry := command.NewEntry("cmd-expired", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)

	// Fix the clock so the entry appears expired immediately.
	fixedNow := created.Add(testtime.D2h)

	scanner := &mockScanner{entries: []command.Entry{expiredEntry}}
	q := &mockAckQueue{}

	s := &command.Sweeper{
		Scanner:  scanner,
		Queue:    q,
		Filter:   command.ScanFilter{DeviceID: "dev-1"},
		Interval: testtime.D10ms,
		Now:      func() time.Time { return fixedNow },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	require.Eventually(t, func() bool {
		return q.CallCount() >= 1
	}, testtime.D2s, testtime.D10ms, "Queue.Ack must have been called at least once")
	cancel()
	<-done

	calls := q.Calls()
	require.NotEmpty(t, calls, "Queue.Ack must have been called at least once")
	assert.Equal(t, "cmd-expired", calls[0].id)
	assert.Equal(t, command.AckTimeout, calls[0].reason)
}

func TestSweeper_Start_PropagatesScanFilter(t *testing.T) {
	defer goleak.VerifyNone(t)

	scanner := &mockScanner{}
	q := &mockAckQueue{}
	filter := command.ScanFilter{DeviceID: "dev-42", Statuses: []command.Status{command.StatusPending}}
	s := &command.Sweeper{
		Scanner:  scanner,
		Queue:    q,
		Filter:   filter,
		Interval: testtime.D10ms,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	require.Eventually(t, func() bool {
		scanner.mu.Lock()
		defer scanner.mu.Unlock()
		return scanner.calls >= 1
	}, testtime.D2s, testtime.D10ms, "Sweeper must call Scanner.ScanActive at least once")
	cancel()
	<-done

	scanner.mu.Lock()
	defer scanner.mu.Unlock()
	assert.Equal(t, filter, scanner.lastFilter, "Sweeper must pass Filter to Scanner.ScanActive")
}

func TestSweeper_Start_OnError_Callback(t *testing.T) {
	defer goleak.VerifyNone(t)

	scannerErr := errors.New("db unavailable")
	scanner := &mockScanner{err: scannerErr}
	q := &mockAckQueue{}

	var (
		errMu     sync.Mutex
		errCalled int
	)
	s := &command.Sweeper{
		Scanner:  scanner,
		Queue:    q,
		Interval: testtime.D10ms,
		OnError: func(err error) {
			errMu.Lock()
			defer errMu.Unlock()
			errCalled++
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	require.Eventually(t, func() bool {
		errMu.Lock()
		defer errMu.Unlock()
		return errCalled >= 1
	}, testtime.D2s, testtime.D10ms, "OnError must be called when Scanner returns error")
	cancel()
	<-done

	errMu.Lock()
	n := errCalled
	errMu.Unlock()
	assert.GreaterOrEqual(t, n, 1, "OnError must be called when Scanner returns error")
}

func TestSweeper_Start_AckErrorForwardedToOnError(t *testing.T) {
	defer goleak.VerifyNone(t)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredEntry := command.NewEntry("cmd-expired", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)

	fixedNow := created.Add(testtime.D2h)

	scanner := &mockScanner{entries: []command.Entry{expiredEntry}}
	q := &mockAckQueue{err: errors.New("ack rejected")}

	var (
		errMu     sync.Mutex
		errCalled int
	)
	s := &command.Sweeper{
		Scanner:  scanner,
		Queue:    q,
		Interval: testtime.D10ms,
		Now:      func() time.Time { return fixedNow },
		OnError: func(err error) {
			errMu.Lock()
			defer errMu.Unlock()
			errCalled++
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	require.Eventually(t, func() bool {
		errMu.Lock()
		defer errMu.Unlock()
		return errCalled >= 1
	}, testtime.D2s, testtime.D10ms, "OnError must be called when Queue.Ack returns error")
	cancel()
	<-done

	errMu.Lock()
	n := errCalled
	errMu.Unlock()
	assert.GreaterOrEqual(t, n, 1, "OnError must be called when Queue.Ack returns error")
}

func TestSweeper_DefaultIntervalAndNow(t *testing.T) {
	t.Parallel()
	// Verify that zero Interval and nil Now don't panic during Start
	// by canceling immediately before any tick fires.
	scanner := &mockScanner{}
	q := &mockAckQueue{}

	s := &command.Sweeper{
		Scanner: scanner,
		Queue:   q,
		// Interval: zero → should default to 30s
		// Now: nil → should default to time.Now
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — no ticks will fire

	// Should return immediately without error (ctx already canceled).
	err := s.Start(ctx)
	assert.NoError(t, err)
}
