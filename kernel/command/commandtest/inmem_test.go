package commandtest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
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
