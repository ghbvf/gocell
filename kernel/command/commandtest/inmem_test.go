package commandtest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLeaseExpiredWindow is used in ExtendLease tests to advance time past
// a 5-minute lease so the test can verify the lease is considered expired.
const testLeaseExpiredWindow = 20 * time.Minute

func TestInMemQueue_ImplementsInterfaces(t *testing.T) {
	t.Parallel()
	var _ command.Queue = (*commandtest.InMemQueue)(nil)
	var _ command.ActiveScanner = (*commandtest.InMemQueue)(nil)
	var _ command.Writer = (*commandtest.InMemQueue)(nil)
}

func makeEntry(id, deviceID string, now time.Time) command.Entry {
	return command.NewEntry(id, deviceID, "reboot", []byte(`{"force":true}`), command.Timeouts{
		OverallDeadline: testtime.D1h,
	}, now)
}

func TestInMemQueue_WriteCommand(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	entry := command.NewEntry("cmd-seeded", "dev-1", "reboot", []byte(`{}`), command.Timeouts{}, now)
	require.NoError(t, q.WriteCommand(ctx, entry))

	got, err := q.GetCommand(ctx, "cmd-seeded")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "cmd-seeded", got.ID)
}

func TestInMemQueue_EnqueueValidates(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	invalid := command.Entry{Status: command.StatusPending}
	err := q.Enqueue(ctx, invalid, command.EnqueueOptions{})
	assert.Error(t, err, "enqueue of invalid entry must fail")
}

func TestInMemQueue_EnqueueIdempotent_SameKey_NoDup(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	entry := makeEntry("cmd-1", "dev-1", now)
	opts := command.EnqueueOptions{IdempotencyKey: "idem-key-1"}

	require.NoError(t, q.Enqueue(ctx, entry, opts))
	// Second enqueue with same key must be a no-op.
	require.NoError(t, q.Enqueue(ctx, entry, opts))

	active, err := q.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-1"})
	require.NoError(t, err)
	assert.Len(t, active, 1, "idempotent enqueue must not create duplicates")
}

func TestInMemQueue_EnqueueAuthzReject(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	entry := makeEntry("cmd-1", "dev-1", now)
	opts := command.EnqueueOptions{
		Authz: func(_ context.Context) error {
			return errors.New("permission denied")
		},
	}
	err := q.Enqueue(ctx, entry, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authz rejected")
}

func TestInMemQueue_DequeueSetsLease(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 10, testtime.D5min)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, command.StatusSent, entries[0].Status)
	assert.Equal(t, 1, entries[0].Attempt)
	assert.NotNil(t, entries[0].SentAt)
}

func TestInMemQueue_DequeueCtxCanceled(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Should still work — InMemQueue is synchronous.
	_, err := q.Dequeue(ctx, "dev-1", 10, testtime.D5min)
	assert.NoError(t, err, "in-mem dequeue is synchronous and ignores canceled ctx")
}

func TestInMemQueue_Dequeue_DefaultLeaseDuration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-1", "dev-1", now), command.EnqueueOptions{}))

	// leaseDuration <= 0 triggers DefaultLeaseDuration fallback.
	entries, err := q.Dequeue(ctx, "dev-1", 1, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// After DefaultLeaseDuration the lease must be considered expired.
	err = q.ExtendLease(ctx, entries[0].ID, time.Minute, now.Add(command.DefaultLeaseDuration+time.Second))
	require.Error(t, err, "lease must be expired past DefaultLeaseDuration")
}

func TestInMemQueue_Report_AdvancesToDelivered(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	cmdID := entries[0].ID

	require.NoError(t, q.Report(ctx, cmdID, now.Add(time.Second)))

	got, err := q.GetCommand(ctx, cmdID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusDelivered, got.Status)
	require.NotNil(t, got.DeliveredAt)
}

func TestInMemQueue_Report_Idempotent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	cmdID := entries[0].ID

	require.NoError(t, q.Report(ctx, cmdID, now.Add(time.Second)))
	// Second Report on already-Delivered is a no-op.
	require.NoError(t, q.Report(ctx, cmdID, now.Add(testtime.D2s)))
}

func TestInMemQueue_Report_FromPending_Fails(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	// Report on Pending (never Dequeued) — invalid transition.
	err := q.Report(ctx, "cmd-1", now)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestInMemQueue_Report_NotFound(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	err := q.Report(context.Background(), "cmd-nonexistent", time.Now())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCommandNotFound, ecErr.Code)
}

func TestInMemQueue_Ack_InvalidReason(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	err := q.Ack(ctx, "cmd-1", command.AckReason(99), now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AckReason")
}

func TestInMemQueue_Ack_SingleStep_SentToSucceeded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	cmdID := entries[0].ID

	// Ack(Success) directly from StatusSent (skipping Report).
	require.NoError(t, q.Ack(ctx, cmdID, command.AckSuccess, now.Add(time.Second)))

	got, err := q.GetCommand(ctx, cmdID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSucceeded, got.Status)
	require.NotNil(t, got.CompletedAt)
	// DeliveredAt must remain nil — Report was skipped.
	assert.Nil(t, got.DeliveredAt,
		"DeliveredAt must be nil when Ack(Success) is called without prior Report")
}

func TestInMemQueue_Ack_DeliveredToSucceeded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	cmdID := entries[0].ID

	require.NoError(t, q.Report(ctx, cmdID, now.Add(time.Second)))
	require.NoError(t, q.Ack(ctx, cmdID, command.AckSuccess, now.Add(testtime.D2s)))

	got, err := q.GetCommand(ctx, cmdID)
	require.NoError(t, err)
	assert.Equal(t, command.StatusSucceeded, got.Status)
	require.NotNil(t, got.DeliveredAt, "DeliveredAt must be set when Report was called")
	require.NotNil(t, got.CompletedAt)
}

func TestInMemQueue_Ack_FailedAndRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		reason   command.AckReason
		wantStat command.Status
	}{
		{command.AckFailed, command.StatusFailed},
		{command.AckRejected, command.StatusCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.reason.String(), func(t *testing.T) {
			t.Parallel()
			q := commandtest.NewInMemQueue()
			q.Now = func() time.Time { return now }
			ctx := context.Background()

			entry := makeEntry("cmd-1", "dev-1", now)
			require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))
			entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
			require.NoError(t, err)

			require.NoError(t, q.Ack(ctx, entries[0].ID, tc.reason, now.Add(time.Second)))
			got, err := q.GetCommand(ctx, entries[0].ID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStat, got.Status)
		})
	}
}

func TestInMemQueue_Ack_Timeout_TerminalExpired_FromAnyNonTerminal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// AckTimeout is the Sweeper-driven terminal: must work from Pending, Sent, or Delivered.
	cases := []struct {
		name    string
		prepare func(q *commandtest.InMemQueue, ctx context.Context, cmdID string) error
	}{
		{"FromPending", func(_ *commandtest.InMemQueue, _ context.Context, _ string) error { return nil }},
		{"FromSent", func(q *commandtest.InMemQueue, ctx context.Context, cmdID string) error {
			_, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
			_ = cmdID
			return err
		}},
		{"FromDelivered", func(q *commandtest.InMemQueue, ctx context.Context, cmdID string) error {
			if _, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min); err != nil {
				return err
			}
			return q.Report(ctx, cmdID, now.Add(time.Second))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := commandtest.NewInMemQueue()
			q.Now = func() time.Time { return now }
			ctx := context.Background()

			entry := makeEntry("cmd-1", "dev-1", now)
			require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))
			require.NoError(t, tc.prepare(q, ctx, "cmd-1"))

			require.NoError(t, q.Ack(ctx, "cmd-1", command.AckTimeout, now.Add(testtime.D2s)))
			got, err := q.GetCommand(ctx, "cmd-1")
			require.NoError(t, err)
			assert.Equal(t, command.StatusExpired, got.Status,
				"AckTimeout must advance to Expired terminal (sweeper-driven)")
		})
	}
}

func TestInMemQueue_ExtendLease_SuccessAndExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 10, testtime.D5min)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	cmdID := entries[0].ID

	// Extend while lease is still valid
	require.NoError(t, q.ExtendLease(ctx, cmdID, testtime.D10min, now.Add(testtime.D1min)))

	// Extend after lease expired — should fail
	err = q.ExtendLease(ctx, cmdID, testtime.D10min, now.Add(testLeaseExpiredWindow))
	assert.Error(t, err, "extending expired lease must fail")
}

func TestInMemQueue_ExtendLease_CommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	err := q.ExtendLease(ctx, "cmd-nonexistent", testtime.D5min, now)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCommandNotFound, ecErr.Code)
}

func TestInMemQueue_ExtendLease_LeaseExpiredVsCommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	cmdID := entries[0].ID

	err = q.ExtendLease(ctx, cmdID, testtime.D5min, now.Add(testtime.D10min))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestInMemQueue_Cancel_Terminal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	require.NoError(t, q.Cancel(ctx, "cmd-1", now.Add(testtime.D1s)))

	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusCanceled, got.Status)

	// Canceling an already-terminal command should fail.
	err = q.Cancel(ctx, "cmd-1", now.Add(testtime.D2s))
	assert.Error(t, err, "canceling terminal command must fail")
}

func TestInMemQueue_ConcurrentDequeue_NoDup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	for i := range 5 {
		entry := command.NewEntry(
			"cmd-"+string(rune('A'+i)),
			"dev-1",
			"reboot",
			[]byte(`{}`),
			command.Timeouts{},
			now.Add(time.Duration(i)*time.Second),
		)
		require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all []command.Entry
	)
	for range 2 {
		wg.Go(func() {
			entries, err := q.Dequeue(ctx, "dev-1", 5, testtime.D5min)
			assert.NoError(t, err)
			mu.Lock()
			all = append(all, entries...)
			mu.Unlock()
		})
	}
	wg.Wait()

	seen := make(map[string]bool)
	for _, e := range all {
		assert.False(t, seen[e.ID], "entry %s dequeued more than once", e.ID)
		seen[e.ID] = true
	}
	assert.LessOrEqual(t, len(all), 5, "total dequeued must not exceed 5")
}

func TestInMemQueue_EnqueueIdempotent_Scalability(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	start := time.Now()

	for i := range 1000 {
		id := "cmd-" + string(rune(i+0x4E00))
		key := "idem-" + id
		entry := command.NewEntry(id, "dev-1", "reboot", []byte(`{}`), command.Timeouts{},
			now.Add(time.Duration(i)*time.Second))
		err := q.Enqueue(ctx, entry, command.EnqueueOptions{IdempotencyKey: key})
		require.NoError(t, err)
	}

	for i := range 500 {
		id := "cmd-" + string(rune(i+0x4E00))
		key := "idem-" + id
		entry := command.NewEntry(id+"-dup", "dev-1", "reboot", []byte(`{}`), command.Timeouts{},
			now.Add(time.Duration(i)*time.Second))
		err := q.Enqueue(ctx, entry, command.EnqueueOptions{IdempotencyKey: key})
		require.NoError(t, err)
	}

	elapsed := time.Since(start)

	active, err := q.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-1"})
	require.NoError(t, err)
	assert.Len(t, active, 1000, "idempotent re-enqueue must not create duplicates")
	assert.Less(t, elapsed, time.Second, "1500 operations must complete in under 1 second")
}

func TestInMemQueue_DefaultClock(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	q.Now = nil // force fallback to time.Now
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", time.Now())
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].SentAt)
}

func TestInMemQueue_Ack_CommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	err := q.Ack(ctx, "cmd-nonexistent", command.AckSuccess, now)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCommandNotFound, ecErr.Code)
}

func TestInMemQueue_Cancel_CommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	err := q.Cancel(ctx, "cmd-nonexistent", now)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCommandNotFound, ecErr.Code)
}

func TestInMemQueue_GetCommand_NotFound(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	got, err := q.GetCommand(ctx, "cmd-nonexistent")
	require.Error(t, err)
	assert.Nil(t, got)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCommandNotFound, ecErr.Code)
}

func TestInMemQueue_ScanActive_Filtering(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	// Unknown device → empty.
	got, err := q.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-unknown"})
	require.NoError(t, err)
	assert.Empty(t, got)

	// Seed: dev-1 has one Pending + one Sent; dev-2 has one Pending.
	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-a", "dev-1", now), command.EnqueueOptions{}))
	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-b", "dev-1", now.Add(time.Second)), command.EnqueueOptions{}))
	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-c", "dev-2", now), command.EnqueueOptions{}))

	// Dequeue one of dev-1's commands → cmd-a moves to Sent.
	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// dev-1 filter → 2 entries (Pending cmd-b + Sent cmd-a, both non-terminal).
	got, err = q.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-1"})
	require.NoError(t, err)
	assert.Len(t, got, 2)

	// dev-1, status Pending only → 1 entry (cmd-b).
	got, err = q.ScanActive(ctx, command.ScanFilter{
		DeviceID: "dev-1",
		Statuses: []command.Status{command.StatusPending},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "cmd-b", got[0].ID)

	// Global (DeviceID="") → 3 entries.
	got, err = q.ScanActive(ctx, command.ScanFilter{})
	require.NoError(t, err)
	assert.Len(t, got, 3)

	// Terminal-only filter is silently ignored → empty.
	got, err = q.ScanActive(ctx, command.ScanFilter{
		Statuses: []command.Status{command.StatusSucceeded, command.StatusFailed},
	})
	require.NoError(t, err)
	assert.Empty(t, got, "filter containing only terminal statuses must return empty")
}

func TestInMemQueue_Ack_AfterTerminal_Fails(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	cmdID := entries[0].ID

	require.NoError(t, q.Ack(ctx, cmdID, command.AckSuccess, now.Add(time.Second)))

	// Second Ack on terminal → transition error wrapped.
	err = q.Ack(ctx, cmdID, command.AckFailed, now.Add(testtime.D2s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already terminal")
}

func TestInMemQueue_Ack_ConcurrentSameReason_Idempotent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-1", "dev-1", now), command.EnqueueOptions{}))
	_, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			errs <- q.Ack(ctx, "cmd-1", command.AckSuccess, now.Add(time.Second))
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSucceeded, got.Status)
}

func TestInMemQueue_Ack_ConcurrentDifferentReason_RejectsLoser(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-1", "dev-1", now), command.EnqueueOptions{}))
	_, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, reason := range []command.AckReason{command.AckSuccess, command.AckFailed} {
		wg.Add(1)
		go func(reason command.AckReason) {
			defer wg.Done()
			errs <- q.Ack(ctx, "cmd-1", reason, now.Add(time.Second))
		}(reason)
	}
	wg.Wait()
	close(errs)

	var successCount, errorCount int
	for err := range errs {
		if err == nil {
			successCount++
			continue
		}
		errorCount++
	}
	assert.Equal(t, 1, successCount)
	assert.Equal(t, 1, errorCount)
}

func TestInMemQueue_ClockInjection(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	current := base

	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return current }

	ctx := context.Background()
	entry := makeEntry("cmd-1", "dev-1", base)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	current = base.Add(testtime.D10s)
	entries, err := q.Dequeue(ctx, "dev-1", 1, testtime.D5min)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, current, *entries[0].SentAt,
		"SentAt must use injected clock, not wall-clock")
}
