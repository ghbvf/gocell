package commandtest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/command"
)

// TestInMemQueue_StripsInternalMetaKeysOnReturn asserts that values returned
// from the InMemQueue read path (GetCommand / Dequeue / ScanActive) never
// expose adapter-internal metadata keys whose name starts with `_`. This is
// the same redaction contract as the PG adapter (F-S-002).
func TestInMemQueue_StripsInternalMetaKeysOnReturn(t *testing.T) {
	t.Parallel()
	q := NewInMemQueue()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	// Enqueue with idempotency key so InMemQueue stamps "_idempotency_key"
	// into the stored entry; user-visible meta also carries a real key.
	entry := command.NewEntry("c-1", "dev-meta", "ping", []byte("x"), command.Timeouts{}, now)
	entry.Metadata = map[string]string{"trace_id": "abc-123"}
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{IdempotencyKey: "k-1"}))

	t.Run("GetCommand", func(t *testing.T) {
		got, err := q.GetCommand(ctx, "c-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "abc-123", got.Metadata["trace_id"], "user-visible key must survive")
		_, present := got.Metadata["_idempotency_key"]
		assert.False(t, present, "adapter-internal _idempotency_key must be stripped")
	})

	t.Run("Dequeue", func(t *testing.T) {
		got, err := q.Dequeue(ctx, "dev-meta", 1, command.DefaultLeaseDuration)
		require.NoError(t, err)
		require.Len(t, got, 1)
		_, present := got[0].Metadata["_idempotency_key"]
		assert.False(t, present, "dequeue must strip _idempotency_key from returned copy")
		assert.Equal(t, "abc-123", got[0].Metadata["trace_id"])
	})

	t.Run("ScanActive", func(t *testing.T) {
		got, err := q.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-meta"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		_, present := got[0].Metadata["_idempotency_key"]
		assert.False(t, present)
	})
}

// TestInMemQueue_OnlyInternalKeysReturnsNilMetadata covers the branch where
// stripInternalMetaKeys ends with an empty result and must collapse Metadata
// to nil (avoids returning {} which downstream slog handlers render as empty
// JSON object).
func TestInMemQueue_OnlyInternalKeysReturnsNilMetadata(t *testing.T) {
	t.Parallel()
	q := NewInMemQueue()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	// No user keys — Enqueue with only the idempotency key.
	entry := command.NewEntry("c-only-internal", "dev-x", "ping", []byte("x"), command.Timeouts{}, now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{IdempotencyKey: "k-2"}))

	got, err := q.GetCommand(ctx, "c-only-internal")
	require.NoError(t, err)
	assert.Nil(t, got.Metadata, "after stripping the lone internal key, Metadata must collapse to nil")
}
