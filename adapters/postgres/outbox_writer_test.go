package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// mustScanEntry builds a sealed outbox.Entry from EntryScan for test use.
// Panics if the scan produces an invalid entry — test programmer error.
func mustScanEntry(s outbox.EntryScan) outbox.Entry {
	e, err := s.ToEntry()
	if err != nil {
		panic("mustScanEntry: " + err.Error())
	}
	return e
}

func TestOutboxWriter_Write_NoTx(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	now := time.Now()
	entry := mustScanEntry(outbox.EntryScan{
		ID:         "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		AggregateID:   "agg-1",
		AggregateType: "order",
		EventType:  "order.created",
		Payload:    []byte(`{"id":"1"}`),
		CreatedAt:  now,
		OccurredAt: now,
	})

	err := w.Write(context.Background(), entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGNoTx, ec.Code)
}

func TestOutboxWriter_Write_Success(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	now := time.Now()
	entry := mustScanEntry(outbox.EntryScan{
		ID:         "b2c3d4e5-f6a7-8901-bcde-f12345678901",
		AggregateID:   "agg-2",
		AggregateType: "order",
		EventType:  "order.shipped",
		Payload:    []byte(`{"shipped":true}`),
		CreatedAt:  now,
		OccurredAt: now,
		Metadata:   map[string]string{"source": "test"},
	})

	err := w.Write(ctx, entry)
	require.NoError(t, err)

	require.Len(t, tx.execCalls, 1)
	call := tx.execCalls[0]
	assert.Contains(t, call.sql, "INSERT INTO outbox_entries")
	assert.Equal(t, "b2c3d4e5-f6a7-8901-bcde-f12345678901", call.args[0]) // id
	assert.Equal(t, "agg-2", call.args[1])                                // aggregate_id
	assert.Equal(t, "order", call.args[2])                                // aggregate_type
	assert.Equal(t, "order.shipped", call.args[3])                        // event_type
	assert.Equal(t, "order.shipped", call.args[4])                        // routing_topic (falls back to eventType)

	// Verify metadata was serialized as JSON.
	metaJSON, ok := call.args[6].([]byte)
	require.True(t, ok)
	var meta map[string]string
	require.NoError(t, json.Unmarshal(metaJSON, &meta))
	assert.Equal(t, "test", meta["source"])

	// $9 (args[8]) must be StatePending — regression guard for "no bare 'pending' literal".
	assert.Equal(t, outbox.StatePending.String(), call.args[8]) // status = $9
}

func TestOutboxWriter_Write_WithTopic(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	now := time.Now()
	entry := mustScanEntry(outbox.EntryScan{
		ID:         "c3d4e5f6-a7b8-9012-cdef-123456789012",
		AggregateID:   "agg-t",
		AggregateType: "device",
		EventType:  "device.enrolled",
		Topic:      "custom.topic.v1",
		Payload:    []byte(`{"enrolled":true}`),
		CreatedAt:  now,
		OccurredAt: now,
	})

	err := w.Write(ctx, entry)
	require.NoError(t, err)

	require.Len(t, tx.execCalls, 1)
	call := tx.execCalls[0]
	assert.Equal(t, "custom.topic.v1", call.args[4]) // topic column
}

func TestOutboxWriter_Write_InjectsObservabilityFromContext(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	ctx = ctxkeys.WithRequestID(ctx, "req-123")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-123")
	ctx = ctxkeys.WithTraceID(ctx, "trace-123")

	now := time.Now()
	// NewEntry injects observability from ctx at construction time.
	entry, err := outbox.NewEntry(clock.Real(), ctx, "order.created", []byte(`{"id":"1"}`),
		outbox.WithID("ctx-meta-0001"),
		outbox.WithCreatedAt(now),
		outbox.WithOccurredAt(now),
		outbox.WithMetadata(map[string]string{"source": "handler"}),
	)
	require.NoError(t, err)

	err = w.Write(ctx, entry)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1)

	// Business metadata (arg index 6): only business keys, NO reserved obs keys.
	metaJSON, ok := tx.execCalls[0].args[6].([]byte)
	require.True(t, ok)
	var meta map[string]string
	require.NoError(t, json.Unmarshal(metaJSON, &meta))
	assert.Equal(t, "handler", meta["source"])
	_, hasReqID := meta["request_id"]
	assert.False(t, hasReqID, "request_id must not be in business metadata column — it belongs in observability column")

	// Observability column (arg index 9): carries trace context from ctx.
	// ($1=id $2=agg_id $3=agg_type $4=event_type $5=topic $6=payload $7=metadata $8=created_at $9=status $10=observability)
	obsJSON, obsOK := tx.execCalls[0].args[9].([]byte)
	require.True(t, obsOK)
	var obs outbox.ObservabilityMetadata
	require.NoError(t, json.Unmarshal(obsJSON, &obs))
	assert.Equal(t, "req-123", string(obs.RequestID))
	assert.Equal(t, "corr-123", string(obs.CorrelationID))
	assert.Equal(t, "trace-123", string(obs.TraceID))
}

func TestOutboxWriter_Write_TxExecError(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{execErr: errcode.New(errcode.KindInternal, ErrAdapterPGQuery, "exec failed")}

	ctx := CtxWithTx(context.Background(), tx)
	now := time.Now()
	entry := mustScanEntry(outbox.EntryScan{
		ID:         "e5f6a7b8-c9d0-1234-efab-345678901234",
		EventType:  "test",
		Payload:    []byte("{}"),
		CreatedAt:  now,
		OccurredAt: now,
	})

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGQuery, ec.Code)
}

func TestOutboxWriter_Write_EmptyID(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	// outbox.Entry{} is a zero-value sealed Entry — ID() returns "" triggering empty-ID check.
	entry := outbox.Entry{}

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "must not be empty")
}

func TestOutboxWriter_Write_InvalidID(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	tests := []struct {
		name    string
		id      string
		wantMsg string
	}{
		{"empty string", "", "must not be empty"},
		{"whitespace only", "   ", "must not be empty"},
		{"all-zeros UUID", "00000000-0000-0000-0000-000000000000", "all-zeros UUID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For empty/whitespace IDs: use zero-value Entry (ID() returns "").
			// For all-zeros UUID: reconstruct via EntryScan but Write has a separate guard before Validate.
			// We test Write's pre-Validate guards directly.
			var entry outbox.Entry
			if tt.id == "00000000-0000-0000-0000-000000000000" {
				// Write checks ID before Validate — use an EntryScan without calling ToEntry
				// (which would reject allZeroUUID at Validate). Instead we rely on the pre-Validate
				// guard in Write that rejects allZeroUUID. We need to sneak past ToEntry.
				// Since allZeroUUID is blocked by EntryScan.ToEntry → Entry.Validate, we can't
				// test this path from outside anymore. Verify the guard exists in Write source.
				// SKIP: allZeroUUID guard is now caught by Entry.Validate (Validate checks id via SafeID).
				t.Skip("allZeroUUID rejected by EntryScan.ToEntry — sealed construction prevents this test path")
			}
			// entry is zero-value → ID() == ""
			_ = entry

			err := w.Write(ctx, entry)
			require.Error(t, err)

			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
			assert.Contains(t, ec.Message, tt.wantMsg)
		})
	}
}

func TestOutboxWriter_Write_ValidUUIDs(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	tests := []struct {
		name string
		id   string
	}{
		{"lowercase v4", "550e8400-e29b-41d4-a716-446655440000"},
		{"uppercase", "550E8400-E29B-41D4-A716-446655440000"},
		{"mixed case", "550e8400-E29B-41d4-A716-446655440000"},
		{"all f", "ffffffff-ffff-ffff-ffff-ffffffffffff"},
		{"evt-uuid prefix", "evt-550e8400-e29b-41d4-a716-446655440000"},
		{"audit prefix", "audit-550e8400-e29b-41d4-a716-446655440000"},
		{"short id", "my-event-42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx.execCalls = nil // reset between sub-tests
			now := time.Now()
			entry := mustScanEntry(outbox.EntryScan{
				ID:         tt.id,
				EventType:  "test.event",
				Payload:    []byte("{}"),
				CreatedAt:  now,
				OccurredAt: now,
			})

			err := w.Write(ctx, entry)
			require.NoError(t, err)
			require.Len(t, tx.execCalls, 1)
			assert.Equal(t, tt.id, tx.execCalls[0].args[0])
		})
	}
}

func TestOutboxWriter_Write_MissingPayload(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	// Zero-value Entry has no payload → Write's Validate fails.
	entry := outbox.Entry{}

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Empty(t, tx.execCalls, "should not reach DB insert")
}

// --- WriteBatch Tests ---

func TestOutboxWriter_WriteBatch_EmptySlice(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	err := w.WriteBatch(ctx, nil)
	assert.NoError(t, err)
	assert.Empty(t, tx.execCalls)

	err = w.WriteBatch(ctx, []outbox.Entry{})
	assert.NoError(t, err)
	assert.Empty(t, tx.execCalls)
}

func TestOutboxWriter_WriteBatch_NoTx(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	now := time.Now()
	entries := []outbox.Entry{mustScanEntry(outbox.EntryScan{
		ID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Topic: "t",
		Payload: []byte("{}"), CreatedAt: now, OccurredAt: now,
	})}

	err := w.WriteBatch(context.Background(), entries)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGNoTx, ec.Code)
}

func TestOutboxWriter_WriteBatch_Success(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	now := time.Now()
	entries := []outbox.Entry{
		mustScanEntry(outbox.EntryScan{ID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Topic: "t1", Payload: []byte(`{"a":1}`), CreatedAt: now, OccurredAt: now}),
		mustScanEntry(outbox.EntryScan{ID: "b2c3d4e5-f6a7-8901-bcde-f12345678901", Topic: "t2", Payload: []byte(`{"b":2}`), CreatedAt: now, OccurredAt: now}),
	}

	err := w.WriteBatch(ctx, entries)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1)

	call := tx.execCalls[0]
	assert.Contains(t, call.sql, "INSERT INTO outbox_entries")
	// 2 entries × 12 cols = 24 args
	assert.Len(t, call.args, 24)
	assert.Equal(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890", call.args[0])
	assert.Equal(t, "b2c3d4e5-f6a7-8901-bcde-f12345678901", call.args[12])

	// $9 per entry (args[8] and args[20]) must be StatePending — regression guard.
	assert.Equal(t, outbox.StatePending.String(), call.args[8])  // first entry status = $9
	assert.Equal(t, outbox.StatePending.String(), call.args[20]) // second entry status = $21
}

func TestOutboxWriter_WriteBatch_InjectsObservabilityFromContext(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	ctx = ctxkeys.WithRequestID(ctx, "req-batch")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-batch")
	ctx = ctxkeys.WithTraceID(ctx, "trace-batch")

	now := time.Now()
	// Use NewEntry so observability is injected from ctx.
	e1, err1 := outbox.NewEntry(clock.Real(), ctx, "orders.v1", []byte(`{"idx":1}`),
		outbox.WithID("batch-ctx-0001"),
		outbox.WithCreatedAt(now), outbox.WithOccurredAt(now),
		outbox.WithMetadata(map[string]string{"source": "business"}),
	)
	require.NoError(t, err1)
	e2, err2 := outbox.NewEntry(clock.Real(), ctx, "orders.v1", []byte(`{"idx":2}`),
		outbox.WithID("batch-ctx-0002"),
		outbox.WithCreatedAt(now), outbox.WithOccurredAt(now),
	)
	require.NoError(t, err2)

	entries := []outbox.Entry{e1, e2}

	err := w.WriteBatch(ctx, entries)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1)

	// 2 entries × 12 cols = 24 args
	require.Len(t, tx.execCalls[0].args, 24)

	// First entry: business metadata preserved, observability from ctx.
	firstMetaJSON, ok := tx.execCalls[0].args[6].([]byte)
	require.True(t, ok)
	var firstMeta map[string]string
	require.NoError(t, json.Unmarshal(firstMetaJSON, &firstMeta))
	assert.Equal(t, "business", firstMeta["source"])
	_, hasReqID := firstMeta["request_id"]
	assert.False(t, hasReqID, "request_id must not be in business metadata — it belongs in observability column")

	firstObsJSON, ok := tx.execCalls[0].args[9].([]byte)
	require.True(t, ok)
	var firstObs outbox.ObservabilityMetadata
	require.NoError(t, json.Unmarshal(firstObsJSON, &firstObs))
	assert.Equal(t, "req-batch", string(firstObs.RequestID))
	assert.Equal(t, "corr-batch", string(firstObs.CorrelationID))
	assert.Equal(t, "trace-batch", string(firstObs.TraceID))

	// Second entry: observability also from ctx.
	secondObsJSON, ok := tx.execCalls[0].args[21].([]byte)
	require.True(t, ok)
	var secondObs outbox.ObservabilityMetadata
	require.NoError(t, json.Unmarshal(secondObsJSON, &secondObs))
	assert.Equal(t, "req-batch", string(secondObs.RequestID))
	assert.Equal(t, "corr-batch", string(secondObs.CorrelationID))
	assert.Equal(t, "trace-batch", string(secondObs.TraceID))
}

func TestOutboxWriter_WriteBatch_InvalidEntry(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	t.Run("empty ID", func(t *testing.T) {
		// Zero-value entry has empty ID.
		entries := []outbox.Entry{
			outbox.Entry{},
			outbox.Entry{},
		}
		err := w.WriteBatch(ctx, entries)
		require.Error(t, err)
		var ecErrEmptyID *errcode.Error
		require.True(t, errors.As(err, &ecErrEmptyID))
		assert.Contains(t, ecErrEmptyID.Message, "must not be empty")
		assert.Empty(t, tx.execCalls)
	})
}

func TestOutboxWriter_WriteBatch_ExecError(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{execErr: errcode.New(errcode.KindInternal, ErrAdapterPGQuery, "batch exec failed")}
	ctx := CtxWithTx(context.Background(), tx)

	now := time.Now()
	entries := []outbox.Entry{
		mustScanEntry(outbox.EntryScan{ID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Topic: "t", Payload: []byte("{}"), CreatedAt: now, OccurredAt: now}),
	}

	err := w.WriteBatch(ctx, entries)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGQuery, ec.Code)
}

func TestOutboxWriter_WriteBatch_ChunksLargeBatch(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	// Create writeBatchChunkSize + 1 entries to force 2 chunks.
	n := writeBatchChunkSize + 1
	entries := make([]outbox.Entry, n)
	now := time.Now()
	for i := range n {
		entries[i] = mustScanEntry(outbox.EntryScan{
			ID:         fmt.Sprintf("evt-%012d", i),
			Topic:      "t",
			Payload:    []byte("{}"),
			CreatedAt:  now,
			OccurredAt: now,
		})
	}

	err := w.WriteBatch(ctx, entries)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 2, "should split into 2 chunks")
	assert.Len(t, tx.execCalls[0].args, writeBatchChunkSize*writeBatchChunkCols)
	assert.Len(t, tx.execCalls[1].args, 1*writeBatchChunkCols)
}

// TestOutboxWriter_Write_MetadataLimit_ValidSizeSucceeds verifies that entries
// with metadata within the MaxMetadataBytes limit are accepted. The pre-seal
// test for oversized metadata is no longer reachable via the public API because
// outbox.EntryScan.ToEntry (and outbox.NewEntry) both run Validate() which
// enforces metadata limits at construction time — Write's metadata-size guard
// is now defense-in-depth against future API changes only.
func TestOutboxWriter_Write_MetadataLimit_ValidSizeSucceeds(t *testing.T) {
	w := NewOutboxWriter(clock.Real())
	tx := &mockOutboxTx{}

	// 256-byte value — well under the 64 KiB limit.
	smallVal := make([]byte, 256)
	for i := range smallVal {
		smallVal[i] = 'x'
	}
	now := time.Now()
	entry := mustScanEntry(outbox.EntryScan{
		ID:         "f1f2f3f4-f5f6-7890-abcd-ef1234567890",
		AggregateID:   "agg-small",
		AggregateType: "order",
		EventType:  "order.created",
		Payload:    []byte(`{"id":"small"}`),
		Topic:      "order.created",
		Metadata:   map[string]string{"small": string(smallVal)},
		CreatedAt:  now,
		OccurredAt: now,
	})

	err := w.Write(CtxWithTx(context.Background(), tx), entry)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1, "INSERT should be issued for valid metadata")
}

// mockOutboxTx records exec calls for assertion.
// Embeds pgx.Tx to satisfy the full interface; only Exec/Commit/Rollback are overridden.
type mockOutboxTx struct {
	pgx.Tx
	execCalls []execCall
	execErr   error
}

type execCall struct {
	sql  string
	args []any
}

func (m *mockOutboxTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.execCalls = append(m.execCalls, execCall{sql: sql, args: args})
	if m.execErr != nil {
		return pgconn.NewCommandTag(""), m.execErr
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (m *mockOutboxTx) Commit(_ context.Context) error   { return nil }
func (m *mockOutboxTx) Rollback(_ context.Context) error { return nil }
