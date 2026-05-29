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
	entry := auditcoretest.NewSessionCreatedEntry("sess-1", "usr-1")

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

// TestNewSessionCreatedEntryShape verifies that the entry payload
// unmarshals to the expected sessionId/userId fields.
func TestNewSessionCreatedEntryShape(t *testing.T) {
	t.Parallel()

	entry := auditcoretest.NewSessionCreatedEntry("sess-abc", "usr-xyz")

	require.Equal(t, "event.session.created.v1", entry.EventType())
	require.NotEmpty(t, entry.ID())

	var payload struct {
		SessionID string `json:"sessionId"`
		UserID    string `json:"userId"`
	}
	require.NoError(t, json.Unmarshal(entry.Payload(), &payload), "payload must be valid JSON")
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

	entry := auditcoretest.NewSessionCreatedEntry("sess-clk", "usr-clk")
	result := handler(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"custom-clock chain must Ack; err=%v", result.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), tail.SeqNo)
}

// TestBuildAuditcoreChainTwoEntries verifies that two distinct entries driven
// through the same chain produce a valid two-entry hash chain: Tail.SeqNo == 2
// and Verify(1, 2) returns valid=true with firstInvalidSeq==-1. Each entry
// uses a distinct sessionID so the content-fingerprint idempotency key differs,
// preventing the second append from being rejected as a duplicate.
func TestBuildAuditcoreChainTwoEntries(t *testing.T) {
	t.Parallel()

	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)

	entry1 := auditcoretest.NewSessionCreatedEntry("sess-chain-1", "usr-chain-1")
	result1 := handler(ctx, entry1)
	require.Equalf(t, outbox.DispositionAck, result1.Disposition,
		"first entry must Ack; disposition=%v err=%v", result1.Disposition, result1.Err)

	entry2 := auditcoretest.NewSessionCreatedEntry("sess-chain-2", "usr-chain-2")
	result2 := handler(ctx, entry2)
	require.Equalf(t, outbox.DispositionAck, result2.Disposition,
		"second entry must Ack; disposition=%v err=%v", result2.Disposition, result2.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err, "store.Tail after two Acks")
	require.Equal(t, int64(2), tail.SeqNo,
		"exactly two entries must be appended after two Acks")

	valid, firstInvalidSeq, err := store.Verify(ctx, 1, tail.SeqNo)
	require.NoError(t, err, "store.Verify after two Appends")
	require.True(t, valid, "two-entry hash chain must be valid; firstInvalidSeq=%d", firstInvalidSeq)
	require.Equal(t, int64(-1), firstInvalidSeq,
		"firstInvalidSeq must be -1 when the two-entry chain is intact")
}
