package command_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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
// Mock helpers
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

// mockAckQueuePartialFail returns an error only for the entry whose CommandID
// matches failID; other calls succeed. Used by TestSweepTick_MixedAckErrors.
type mockAckQueuePartialFail struct {
	mu     sync.Mutex
	calls  int
	failID string
	err    error
}

func (a *mockAckQueuePartialFail) Ack(_ context.Context, id string, _ command.AckReason, _ time.Time) error {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	if id == a.failID {
		return a.err
	}
	return nil
}

func (a *mockAckQueuePartialFail) CallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *mockAckQueuePartialFail) Enqueue(context.Context, command.Entry, command.EnqueueOptions) error {
	return errors.New("unused")
}

func (a *mockAckQueuePartialFail) Dequeue(context.Context, string, int, time.Duration) ([]command.Entry, error) {
	return nil, errors.New("unused")
}

func (a *mockAckQueuePartialFail) Report(context.Context, string, time.Time) error {
	return errors.New("unused")
}

func (a *mockAckQueuePartialFail) ExtendLease(context.Context, string, time.Duration, time.Time) error {
	return errors.New("unused")
}

func (a *mockAckQueuePartialFail) Cancel(context.Context, string, time.Time) error {
	return errors.New("unused")
}

var _ command.Queue = (*mockAckQueuePartialFail)(nil)

// ---------------------------------------------------------------------------
// SweepTick tests (new API — B1-a: replaces runTick/Start)
// ---------------------------------------------------------------------------

// TestSweepTick_ScannerError verifies that when scanner returns an error,
// SweepTick returns that error (no longer silently calls onError).
func TestSweepTick_ScannerError(t *testing.T) {
	t.Parallel()
	scanErr := errors.New("db unavailable")
	scanner := &mockScanner{err: scanErr}
	q := &mockAckQueue{}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	got := s.SweepTick(context.Background(), now)
	require.Error(t, got)
	assert.ErrorIs(t, got, scanErr)
}

// TestSweepTick_AckError verifies that a single Ack error is returned.
func TestSweepTick_AckError(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredEntry := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	scanner := &mockScanner{entries: []command.Entry{expiredEntry}}

	ackErr := errors.New("ack rejected")
	q := &mockAckQueue{err: ackErr}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := created.Add(testtime.D5min) // past deadline
	got := s.SweepTick(context.Background(), now)
	require.Error(t, got)
	assert.ErrorIs(t, got, ackErr)
}

// TestSweepTick_MultipleAckErrors verifies errors.Join aggregates all Ack errors.
func TestSweepTick_MultipleAckErrors(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e1 := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	e2 := command.NewEntry("cmd-2", "dev-2", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	scanner := &mockScanner{entries: []command.Entry{e1, e2}}

	ackErr := errors.New("ack rejected")
	q := &mockAckQueue{err: ackErr}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := created.Add(testtime.D5min)
	got := s.SweepTick(context.Background(), now)
	require.Error(t, got)
	// Both Ack errors should be joined
	assert.ErrorIs(t, got, ackErr)
	// Two ack calls were made
	assert.Equal(t, 2, q.CallCount())
}

// TestSweepTick_ScannerErrorShortCircuits verifies that when scanner returns
// an error, SweepTick returns that error immediately and makes no Ack calls.
func TestSweepTick_ScannerErrorShortCircuits(t *testing.T) {
	t.Parallel()
	scanErr := errors.New("scan failed")
	scanner := &mockScanner{err: scanErr}
	q := &mockAckQueue{}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := time.Now()
	got := s.SweepTick(context.Background(), now)
	require.Error(t, got)
	assert.ErrorIs(t, got, scanErr)
	// No Ack calls when scanner fails (short-circuit).
	assert.Equal(t, 0, q.CallCount())
}

// TestSweepTick_MixedAckErrors verifies that when scanner succeeds and returns
// multiple entries, but some Ack calls fail, the errors are aggregated via
// errors.Join. Successful Ack calls are not aborted by a failed one.
func TestSweepTick_MixedAckErrors(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e1 := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	e2 := command.NewEntry("cmd-2", "dev-2", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	e3 := command.NewEntry("cmd-3", "dev-3", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	scanner := &mockScanner{entries: []command.Entry{e1, e2, e3}}

	// cmd-2 Ack fails; cmd-1 and cmd-3 succeed.
	ackErr := errors.New("cmd-2 ack rejected")
	q := &mockAckQueuePartialFail{failID: "cmd-2", err: ackErr}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := created.Add(testtime.D5min) // all past deadline
	got := s.SweepTick(context.Background(), now)
	// Only the Ack error for cmd-2 is returned; scan was successful.
	require.Error(t, got)
	assert.ErrorIs(t, got, ackErr)
	// All 3 Ack calls were made (scan error does not abort remaining calls).
	assert.Equal(t, 3, q.CallCount())
}

// TestSweepTick_NoExpiredEntries verifies nil error when nothing expires.
func TestSweepTick_NoExpiredEntries(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1h,
	}, created)
	scanner := &mockScanner{entries: []command.Entry{e}}
	q := &mockAckQueue{}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := created.Add(testtime.D30min) // before deadline
	got := s.SweepTick(context.Background(), now)
	assert.NoError(t, got)
	assert.Equal(t, 0, q.CallCount())
}

// TestSweepTick_NilReceiverReturnsError pins the nil-receiver guard in SweepTick.
func TestSweepTick_NilReceiverReturnsError(t *testing.T) {
	t.Parallel()
	var s *command.Sweeper
	err := s.SweepTick(context.Background(), time.Now())
	require.Error(t, err)
}

// TestSweepTick_ZeroValueLiteralReturnsError pins built sentinel guard in SweepTick.
func TestSweepTick_ZeroValueLiteralReturnsError(t *testing.T) {
	t.Parallel()
	var s command.Sweeper
	err := s.SweepTick(context.Background(), time.Now())
	require.Error(t, err)
}

// TestNewSweeper_NewSignatureNoClockParam verifies the new signature
// (scanner, queue, opts...) — no clock parameter.
func TestNewSweeper_NewSignatureNoClockParam(t *testing.T) {
	t.Parallel()
	s, err := command.NewSweeper(&mockScanner{}, &mockAckQueue{})
	require.NoError(t, err)
	require.NotNil(t, s)
}

// TestNewSweeper_TypedNilScannerRejected pins the typed-nil interface guard
// for the scanner positional dep.
func TestNewSweeper_TypedNilScannerRejected(t *testing.T) {
	t.Parallel()
	var s *mockScanner
	var scanner command.ActiveScanner = s
	_, err := command.NewSweeper(scanner, &mockAckQueue{})
	require.Error(t, err, "NewSweeper must reject typed-nil scanner")
}

func TestNewSweeper_TypedNilQueueRejected(t *testing.T) {
	t.Parallel()
	var q *mockAckQueue
	var queue command.Queue = q
	_, err := command.NewSweeper(&mockScanner{}, queue)
	require.Error(t, err, "NewSweeper must reject typed-nil queue")
}

// TestSweepTick_AcksExpiredEntries verifies that Ack is called for expired entries.
func TestSweepTick_AcksExpiredEntries(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredEntry := command.NewEntry("cmd-expired", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)
	scanner := &mockScanner{entries: []command.Entry{expiredEntry}}
	q := &mockAckQueue{}

	s, err := command.NewSweeper(scanner, q)
	require.NoError(t, err)

	now := created.Add(testtime.D5min) // past deadline
	require.NoError(t, s.SweepTick(context.Background(), now))
	calls := q.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "cmd-expired", calls[0].id)
	assert.Equal(t, command.AckTimeout, calls[0].reason)
}

// TestSweepTick_FilterPropagated verifies filter is passed to scanner.
func TestSweepTick_FilterPropagated(t *testing.T) {
	t.Parallel()
	scanner := &mockScanner{}
	q := &mockAckQueue{}
	filter := command.ScanFilter{DeviceID: "dev-42", Statuses: []command.Status{command.StatusPending}}

	s, err := command.NewSweeper(scanner, q, command.WithSweeperFilter(filter))
	require.NoError(t, err)

	require.NoError(t, s.SweepTick(context.Background(), time.Now()))
	scanner.mu.Lock()
	defer scanner.mu.Unlock()
	assert.Equal(t, filter, scanner.lastFilter)
}

// TestSweeper_StartZeroValueLiteralRejected and TestSweeper_StartNilReceiverRejected
// were removed as duplicates of TestSweepTick_ZeroValueLiteralReturnsError and
// TestSweepTick_NilReceiverReturnsError (same assertions, same code paths).
