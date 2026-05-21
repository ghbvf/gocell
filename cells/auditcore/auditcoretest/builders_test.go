package auditcoretest_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/auditcoretest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

func TestBuildAuditcoreChain_DefaultWiring(t *testing.T) {
	t.Parallel()
	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)

	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-default", "usr-default")
	result := handler(ctx, entry)

	require.Equalf(t, outbox.DispositionAck, result.Disposition,
		"handler must Ack; got disposition=%v error=%v", result.Disposition, result.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, tail.SeqNo, int64(1), "ledger must have at least one entry after Ack")
}

func TestBuildAuditcoreChain_HMACSignature(t *testing.T) {
	t.Parallel()
	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)

	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-hmac", "usr-hmac")
	result := handler(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition)

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), tail.SeqNo)

	valid, firstInvalidSeq, err := store.Verify(ctx, 1, tail.SeqNo)
	require.NoError(t, err)
	assert.True(t, valid, "HMAC chain must verify clean; firstInvalidSeq=%d", firstInvalidSeq)
	assert.Equal(t, int64(-1), firstInvalidSeq)
}

func TestBuildAuditcoreChain_MultipleEntries(t *testing.T) {
	t.Parallel()
	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)

	entries := []outbox.Entry{
		auditcoretest.CanonicalSessionCreatedEntry("sess-multi-1", "usr-multi"),
		auditcoretest.CanonicalSessionCreatedEntry("sess-multi-2", "usr-multi"),
		auditcoretest.CanonicalSessionCreatedEntry("sess-multi-3", "usr-multi"),
	}

	for i, e := range entries {
		result := handler(ctx, e)
		require.Equalf(t, outbox.DispositionAck, result.Disposition,
			"entry %d must Ack; got disposition=%v error=%v", i, result.Disposition, result.Err)
	}

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), tail.SeqNo, "all three entries must be appended")
	assert.Equal(t, int64(3), tail.EntryCount)

	valid, firstInvalidSeq, err := store.Verify(ctx, 1, tail.SeqNo)
	require.NoError(t, err)
	assert.True(t, valid, "hash chain must be intact for all entries; firstInvalidSeq=%d", firstInvalidSeq)
	assert.Equal(t, int64(-1), firstInvalidSeq)
}

func TestCanonicalSessionCreatedEntry_PayloadShape(t *testing.T) {
	t.Parallel()
	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-shape", "usr-shape")

	assert.NotEmpty(t, entry.ID)
	assert.Equal(t, "event.session.created.v1", entry.EventType)
	require.NotEmpty(t, entry.Payload)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(entry.Payload, &payload), "payload must be valid JSON object")

	assert.Equal(t, "sess-shape", payload["sessionId"], "payload must contain sessionId")
	assert.Equal(t, "usr-shape", payload["userId"], "payload must contain userId")
}

func TestBuildAuditcoreChain_WithEmitter(t *testing.T) {
	t.Parallel()
	rec := outboxtest.NewRecorder()
	handler, _, ctx := auditcoretest.BuildAuditcoreChain(t, auditcoretest.WithEmitter(rec))

	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-rec", "usr-rec")
	result := handler(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition)

	// Recorder captures emitted entries (if auditcore emits anything).
	// The primary assertion is that the custom emitter option is accepted without error.
	_ = rec.Entries()
}
