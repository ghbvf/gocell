package auditcoretest_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/auditcoretest"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestBuildAuditcoreChainSmoke verifies that the default chain wiring is
// functional: handler acks the event, Tail reflects one entry, and Verify
// confirms chain integrity.
func TestBuildAuditcoreChainSmoke(t *testing.T) {
	t.Parallel()

	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)
	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-1", "usr-1")

	result := handler(ctx, entry)
	require.Equalf(t, outbox.DispositionAck, result.Disposition,
		"handler must Ack canonical session.created entry; disposition=%v err=%v",
		result.Disposition, result.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err, "store.Tail after Ack")
	require.Equal(t, int64(1), tail.SeqNo,
		"exactly one entry must be appended after one Ack")

	valid, firstInvalidSeq, err := store.Verify(ctx, 1, tail.SeqNo)
	require.NoError(t, err, "store.Verify after Append")
	require.True(t, valid, "hash chain must be valid; firstInvalidSeq=%d", firstInvalidSeq)
	require.Equal(t, int64(-1), firstInvalidSeq, "firstInvalidSeq must be -1 when chain is intact")
}

// TestCanonicalSessionCreatedEntryShape verifies that the entry payload
// unmarshals to the expected sessionId/userId fields.
func TestCanonicalSessionCreatedEntryShape(t *testing.T) {
	t.Parallel()

	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-abc", "usr-xyz")

	require.Equal(t, "event.session.created.v1", entry.EventType)
	require.NotEmpty(t, entry.ID)

	var payload struct {
		SessionID string `json:"sessionId"`
		UserID    string `json:"userId"`
	}
	require.NoError(t, json.Unmarshal(entry.Payload, &payload), "payload must be valid JSON")
	require.Equal(t, "sess-abc", payload.SessionID)
	require.Equal(t, "usr-xyz", payload.UserID)
}

// TestBuildAuditcoreChainCustomClock verifies that WithChainClock builds
// without panic and the chain remains functional.
func TestBuildAuditcoreChainCustomClock(t *testing.T) {
	t.Parallel()

	testAnchor := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	fakeClock := clockmock.New(testAnchor)

	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t,
		auditcoretest.WithChainClock(fakeClock))

	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-clk", "usr-clk")
	result := handler(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"custom-clock chain must Ack; err=%v", result.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), tail.SeqNo)
}

// TestBuildAuditcoreChainCustomHMACKey verifies that WithChainHMACKey builds
// without panic and the chain remains functional with a different key.
func TestBuildAuditcoreChainCustomHMACKey(t *testing.T) {
	t.Parallel()

	differentKey := []byte("different-hmac-key-32bytes-xxxxx")

	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t,
		auditcoretest.WithChainHMACKey(differentKey))

	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-hmac", "usr-hmac")
	result := handler(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"custom-hmac chain must Ack; err=%v", result.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), tail.SeqNo)
	// Verify that the chain is consistent with its own HMAC key (not the default key).
	valid, _, err := store.Verify(ctx, 1, tail.SeqNo)
	require.NoError(t, err)
	require.True(t, valid, "chain must verify with its own HMAC key")
}
