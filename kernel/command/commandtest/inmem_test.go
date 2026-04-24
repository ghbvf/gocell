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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemQueue_ImplementsQueueInterface(t *testing.T) {
	t.Parallel()
	var _ command.Queue = (*commandtest.InMemQueue)(nil)
	var _ command.Reader = (*commandtest.InMemQueue)(nil)
	var _ command.Writer = (*commandtest.InMemQueue)(nil)
	var _ command.StateAdvancer = (*commandtest.InMemQueue)(nil)
}

func TestInMemQueue_WriteCommand(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	// WriteCommand seeds an entry bypassing Enqueue validation.
	entry := command.NewEntry("cmd-seeded", "dev-1", "reboot", []byte(`{}`), command.Timeouts{}, now)
	require.NoError(t, q.WriteCommand(ctx, entry))

	got, err := q.GetCommand(ctx, "cmd-seeded")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "cmd-seeded", got.ID)
}

func TestInMemQueue_AdvanceStatus(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	// Advance Pending → Sent
	require.NoError(t, q.AdvanceStatus(ctx, "cmd-1", command.StatusPending, command.StatusSent, now))

	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSent, got.Status)
}

func TestInMemQueue_AdvanceStatus_WrongFrom(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	// Advance with wrong 'from' status — should fail (optimistic lock).
	err := q.AdvanceStatus(ctx, "cmd-1", command.StatusSent, command.StatusDelivered, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "optimistic lock")
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

func TestInMemQueue_AckFailed_AckRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	for _, reason := range []command.AckReason{command.AckFailed, command.AckRejected} {
		reason := reason
		t.Run(reason.String(), func(t *testing.T) {
			t.Parallel()
			q2 := commandtest.NewInMemQueue()
			q2.Now = func() time.Time { return now }
			entry := makeEntry("cmd-1", "dev-1", now)
			require.NoError(t, q2.Enqueue(ctx, entry, command.EnqueueOptions{}))

			entries, err := q2.Dequeue(ctx, "dev-1", 1, 5*time.Minute)
			require.NoError(t, err)
			require.Len(t, entries, 1)

			require.NoError(t, q2.Ack(ctx, entries[0].ID, reason, now.Add(time.Second)))

			got, err := q2.GetCommand(ctx, entries[0].ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.True(t, got.Status.IsTerminal())
		})
	}
}

func makeEntry(id, deviceID string, now time.Time) command.Entry {
	return command.NewEntry(id, deviceID, "reboot", []byte(`{"force":true}`), command.Timeouts{
		OverallDeadline: 1 * time.Hour,
	}, now)
}

func TestInMemQueue_EnqueueValidates(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	// Entry with missing ID — Enqueue should assign one via NewEntry+ValidateNew path.
	// But passing a raw Entry with missing required fields fails ValidateNew.
	invalid := command.Entry{
		// ID missing, DeviceID missing, etc.
		Status: command.StatusPending,
	}
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

	pending, err := q.PendingCommands(ctx, "dev-1")
	require.NoError(t, err)
	assert.Len(t, pending, 1, "idempotent enqueue must not create duplicates")
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

	entries, err := q.Dequeue(ctx, "dev-1", 10, 5*time.Minute)
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
	cancel() // already cancelled
	// Should still work — InMemQueue is synchronous.
	_, err := q.Dequeue(ctx, "dev-1", 10, 5*time.Minute)
	assert.NoError(t, err, "in-mem dequeue is synchronous and ignores cancelled ctx")
}

func TestInMemQueue_AckSuccess_FullFlow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 10, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// Ack with success
	require.NoError(t, q.Ack(ctx, entries[0].ID, command.AckSuccess, now.Add(1*time.Second)))

	// Verify terminal state
	got, err := q.GetCommand(ctx, entries[0].ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSucceeded, got.Status)
}

func TestInMemQueue_AckTimeout_ResetsForRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 10, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	cmdID := entries[0].ID

	// AckTimeout should reset to Pending
	require.NoError(t, q.Ack(ctx, cmdID, command.AckTimeout, now.Add(1*time.Second)))

	got, err := q.GetCommand(ctx, cmdID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusPending, got.Status)
	assert.Nil(t, got.SentAt)
	assert.Equal(t, 1, got.Attempt, "Attempt preserved across AckTimeout")
}

func TestInMemQueue_ExtendLease_SuccessAndExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 10, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	cmdID := entries[0].ID

	// Extend while lease is still valid
	require.NoError(t, q.ExtendLease(ctx, cmdID, 10*time.Minute, now.Add(1*time.Minute)))

	// Extend after lease expired — should fail
	err = q.ExtendLease(ctx, cmdID, 10*time.Minute, now.Add(20*time.Minute))
	assert.Error(t, err, "extending expired lease must fail")
}

func TestInMemQueue_Cancel_Terminal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	require.NoError(t, q.Cancel(ctx, "cmd-1", now.Add(1*time.Second)))

	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusCanceled, got.Status)

	// Canceling an already-terminal command should fail.
	err = q.Cancel(ctx, "cmd-1", now.Add(2*time.Second))
	assert.Error(t, err, "canceling terminal command must fail")
}

func TestInMemQueue_ConcurrentDequeue_NoDup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	// Enqueue 5 entries for the same device.
	for i := 0; i < 5; i++ {
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

	// Two goroutines each try to dequeue up to 5 entries.
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all []command.Entry
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries, err := q.Dequeue(ctx, "dev-1", 5, 5*time.Minute)
			assert.NoError(t, err)
			mu.Lock()
			all = append(all, entries...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Count unique IDs — no entry should be dequeued twice.
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

	// Insert 1000 entries with distinct idempotency keys.
	for i := 0; i < 1000; i++ {
		id := "cmd-" + string(rune(i+0x4E00)) // use unique runes to avoid collisions
		key := "idem-" + id
		entry := command.NewEntry(id, "dev-1", "reboot", []byte(`{}`), command.Timeouts{},
			now.Add(time.Duration(i)*time.Second))
		err := q.Enqueue(ctx, entry, command.EnqueueOptions{IdempotencyKey: key})
		require.NoError(t, err)
	}

	// Re-insert 500 duplicate idempotency keys — should all be no-ops.
	for i := 0; i < 500; i++ {
		id := "cmd-" + string(rune(i+0x4E00))
		key := "idem-" + id
		entry := command.NewEntry(id+"-dup", "dev-1", "reboot", []byte(`{}`), command.Timeouts{},
			now.Add(time.Duration(i)*time.Second))
		err := q.Enqueue(ctx, entry, command.EnqueueOptions{IdempotencyKey: key})
		require.NoError(t, err)
	}

	elapsed := time.Since(start)

	// Total entries must still be 1000 (no duplicates).
	pending, err := q.ListPending(ctx, "dev-1", 0)
	require.NoError(t, err)
	assert.Len(t, pending, 1000, "idempotent re-enqueue must not create duplicates")
	assert.Less(t, elapsed, time.Second, "1500 operations must complete in under 1 second")
}

func TestInMemQueue_ExtendLease_CommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	// Extend lease for a command that does not exist at all.
	err := q.ExtendLease(ctx, "cmd-nonexistent", 5*time.Minute, now)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCommandNotFound, ecErr.Code,
		"extending a non-existent command must return ErrCommandNotFound")
}

func TestInMemQueue_ExtendLease_LeaseExpiredVsCommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	// Dequeue to acquire a lease.
	entries, err := q.Dequeue(ctx, "dev-1", 1, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	cmdID := entries[0].ID

	// Attempt to extend past expiry — must return ErrValidationFailed (lease expired).
	err = q.ExtendLease(ctx, cmdID, 5*time.Minute, now.Add(10*time.Minute))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code,
		"extending an expired lease must return ErrValidationFailed, not ErrCommandNotFound")
}

func TestInMemQueue_DefaultClock(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	q.Now = nil // force fallback to time.Now
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", time.Now())
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, 5*time.Minute)
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

func TestInMemQueue_AdvanceStatus_CommandNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	ctx := context.Background()

	err := q.AdvanceStatus(ctx, "cmd-nonexistent", command.StatusPending, command.StatusSent, now)
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
	require.NoError(t, err)
	assert.Nil(t, got, "GetCommand on missing id must return (nil, nil)")
}

func TestInMemQueue_PendingCommands_EmptyAndFilter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	// Unknown device → empty.
	got, err := q.PendingCommands(ctx, "dev-unknown")
	require.NoError(t, err)
	assert.Empty(t, got)

	// Seed entries for two devices.
	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-a", "dev-1", now), command.EnqueueOptions{}))
	require.NoError(t, q.Enqueue(ctx, makeEntry("cmd-b", "dev-2", now), command.EnqueueOptions{}))

	got1, err := q.PendingCommands(ctx, "dev-1")
	require.NoError(t, err)
	assert.Len(t, got1, 1)
	assert.Equal(t, "cmd-a", got1[0].ID)

	got2, err := q.PendingCommands(ctx, "dev-2")
	require.NoError(t, err)
	assert.Len(t, got2, 1)
	assert.Equal(t, "cmd-b", got2[0].ID)
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

func TestInMemQueue_ListPending_NoLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id := "cmd-" + string(rune('A'+i))
		require.NoError(t, q.Enqueue(ctx, makeEntry(id, "dev-1", now.Add(time.Duration(i)*time.Second)),
			command.EnqueueOptions{}))
	}

	// limit <= 0 means "all".
	got, err := q.ListPending(ctx, "dev-1", 0)
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestInMemQueue_Ack_AfterTerminal_Fails(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	entries, err := q.Dequeue(ctx, "dev-1", 1, 5*time.Minute)
	require.NoError(t, err)
	cmdID := entries[0].ID

	// First Ack → Succeeded
	require.NoError(t, q.Ack(ctx, cmdID, command.AckSuccess, now.Add(time.Second)))

	// Second Ack with AckFailed on terminal → transition error wrapped
	err = q.Ack(ctx, cmdID, command.AckFailed, now.Add(2*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "advance to Failed")
}

func TestInMemQueue_AckTimeout_OnPending_Fails(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := commandtest.NewInMemQueue()
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	entry := makeEntry("cmd-1", "dev-1", now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	// AckTimeout on Pending (never Dequeued) must fail.
	err := q.Ack(ctx, "cmd-1", command.AckTimeout, now.Add(time.Second))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
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

	// Advance clock before dequeue.
	current = base.Add(10 * time.Second)
	entries, err := q.Dequeue(ctx, "dev-1", 1, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, current, *entries[0].SentAt,
		"SentAt must use injected clock, not wall-clock")
}
