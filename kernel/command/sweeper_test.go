package command_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

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
		s := s
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
				OverallDeadline: 1 * time.Second,
			}, created)
			// Force terminal status
			e.Status = s

			// now is well past any deadline
			now := created.Add(10 * time.Minute)
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
	now := created.Add(10 * time.Hour)
	result := command.SweepOnce([]command.Entry{e}, now)
	assert.Nil(t, result)
}

func TestSweepOnce_ScheduleToSendExpired(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		ScheduleToSend: 30 * time.Second,
	}, created)
	// now is past ScheduleToSend deadline
	now := created.Add(60 * time.Second)
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
		SendToComplete: 5 * time.Minute,
	}, created)
	// Advance to Sent
	sentAt := created.Add(10 * time.Second)
	require.NoError(t, command.AdvanceCommand(&e, command.StatusSent, sentAt))

	// now is past SendToComplete deadline (sentAt + 5 min)
	now := sentAt.Add(10 * time.Minute)
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
		OverallDeadline: 1 * time.Hour,
	}, created)
	// now is past OverallDeadline
	now := created.Add(2 * time.Hour)
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
		ScheduleToSend:  30 * time.Second,
		OverallDeadline: 1 * time.Minute,
	}, created)
	// now exceeds both
	now := created.Add(5 * time.Minute)
	result := command.SweepOnce([]command.Entry{e}, now)
	require.Len(t, result, 1)
	// Overall wins
	assert.Equal(t, "phase_overall_deadline", result[0].Reason)
}

func TestSweepOnce_NotYetExpired(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: 1 * time.Hour,
	}, created)
	// now is before deadline
	now := created.Add(30 * time.Minute)
	result := command.SweepOnce([]command.Entry{e}, now)
	assert.Nil(t, result)
}

func TestSweepOnce_SendToCompleteIgnoredForPending(t *testing.T) {
	t.Parallel()
	// Pending entry cannot trigger SendToComplete because SentAt is nil.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		SendToComplete: 5 * time.Minute,
	}, created)
	// e.Status == Pending, SentAt == nil
	now := created.Add(10 * time.Minute)
	result := command.SweepOnce([]command.Entry{e}, now)
	assert.Nil(t, result, "SendToComplete cannot trigger for Pending entry (SentAt is nil)")
}

// ---------------------------------------------------------------------------
// Sweeper integration tests
// ---------------------------------------------------------------------------

// mockSweeperReader is an in-test Reader that returns a fixed set of entries.
type mockSweeperReader struct {
	mu      sync.Mutex
	entries []command.Entry
	err     error
	calls   int
}

func (r *mockSweeperReader) PendingCommands(_ context.Context, _ string) ([]command.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return append([]command.Entry(nil), r.entries...), r.err
}

func (r *mockSweeperReader) GetCommand(_ context.Context, _ string) (*command.Entry, error) {
	return nil, nil
}

// mockSweeperAdvancer records AdvanceStatus calls.
type mockSweeperAdvancer struct {
	mu    sync.Mutex
	calls []advanceCall
}

type advanceCall struct {
	id   string
	from command.Status
	to   command.Status
}

func (a *mockSweeperAdvancer) AdvanceStatus(_ context.Context, id string, from, to command.Status, _ time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, advanceCall{id: id, from: from, to: to})
	return nil
}

func (a *mockSweeperAdvancer) CallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *mockSweeperAdvancer) Calls() []advanceCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]advanceCall(nil), a.calls...)
}

func TestSweeper_Start_RequiresDeviceID(t *testing.T) {
	t.Parallel()
	s := &command.Sweeper{
		Reader:   &mockSweeperReader{},
		Advancer: &mockSweeperAdvancer{},
		Interval: 10 * time.Millisecond,
		DeviceID: "", // empty — should return error
	}
	err := s.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DeviceID")
}

func TestSweeper_Start_CtxCancelExits(t *testing.T) {
	defer goleak.VerifyNone(t)
	reader := &mockSweeperReader{}
	advancer := &mockSweeperAdvancer{}
	s := &command.Sweeper{
		Reader:   reader,
		Advancer: advancer,
		Interval: 10 * time.Millisecond,
		DeviceID: "dev-1",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	// Let it run one tick then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	err := <-done
	assert.NoError(t, err)
}

func TestSweeper_Stop_Idempotent(t *testing.T) {
	t.Parallel()
	s := &command.Sweeper{
		Reader:   &mockSweeperReader{},
		Advancer: &mockSweeperAdvancer{},
		DeviceID: "dev-1",
	}
	// Stop is a no-op regardless of state.
	assert.NoError(t, s.Stop(context.Background()))
	assert.NoError(t, s.Stop(context.Background()))
}

func TestSweeper_Start_InvokesAdvancer(t *testing.T) {
	defer goleak.VerifyNone(t)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredEntry := command.NewEntry("cmd-expired", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: 1 * time.Minute,
	}, created)

	// Fix the clock so the entry appears expired immediately.
	fixedNow := created.Add(2 * time.Hour)

	reader := &mockSweeperReader{
		entries: []command.Entry{expiredEntry},
	}
	advancer := &mockSweeperAdvancer{}

	s := &command.Sweeper{
		Reader:   reader,
		Advancer: advancer,
		Interval: 10 * time.Millisecond,
		DeviceID: "dev-1",
		Now:      func() time.Time { return fixedNow },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// Wait for at least one tick to be processed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if advancer.CallCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	calls := advancer.Calls()
	require.NotEmpty(t, calls, "Advancer must have been called at least once")
	assert.Equal(t, "cmd-expired", calls[0].id)
	assert.Equal(t, command.StatusPending, calls[0].from)
	assert.Equal(t, command.StatusExpired, calls[0].to)
}

func TestSweeper_Start_OnError_Callback(t *testing.T) {
	defer goleak.VerifyNone(t)

	readerErr := errors.New("db unavailable")
	reader := &mockSweeperReader{err: readerErr}
	advancer := &mockSweeperAdvancer{}

	var (
		errMu     sync.Mutex
		errCalled int
	)
	s := &command.Sweeper{
		Reader:   reader,
		Advancer: advancer,
		Interval: 10 * time.Millisecond,
		DeviceID: "dev-1",
		OnError: func(err error) {
			errMu.Lock()
			defer errMu.Unlock()
			errCalled++
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// Wait for at least one error callback.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		errMu.Lock()
		n := errCalled
		errMu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	errMu.Lock()
	n := errCalled
	errMu.Unlock()
	assert.GreaterOrEqual(t, n, 1, "OnError must be called when Reader returns error")
}

func TestSweeper_DefaultIntervalAndNow(t *testing.T) {
	t.Parallel()
	// Verify that zero Interval and nil Now don't panic during Start
	// by cancelling immediately before any tick fires.
	reader := &mockSweeperReader{}
	advancer := &mockSweeperAdvancer{}

	s := &command.Sweeper{
		Reader:   reader,
		Advancer: advancer,
		// Interval: zero → should default to 30s
		// Now: nil → should default to time.Now
		DeviceID: "dev-1",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — no ticks will fire

	// Should return immediately without error (ctx already cancelled).
	err := s.Start(ctx)
	assert.NoError(t, err)
}
