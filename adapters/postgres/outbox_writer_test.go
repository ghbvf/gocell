package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxWriter_Write_NoTx(t *testing.T) {
	w := NewOutboxWriter()
	entry := outbox.Entry{
		ID:            "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		AggregateID:   "agg-1",
		AggregateType: "order",
		EventType:     "order.created",
		Payload:       []byte(`{"id":"1"}`),
		CreatedAt:     time.Now(),
	}

	err := w.Write(context.Background(), entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGNoTx, ec.Code)
}

func TestOutboxWriter_Write_Success(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:            "b2c3d4e5-f6a7-8901-bcde-f12345678901",
		AggregateID:   "agg-2",
		AggregateType: "order",
		EventType:     "order.shipped",
		Payload:       []byte(`{"shipped":true}`),
		CreatedAt:     time.Now(),
		Metadata:      map[string]string{"source": "test"},
	}

	err := w.Write(ctx, entry)
	require.NoError(t, err)

	require.Len(t, tx.execCalls, 1)
	call := tx.execCalls[0]
	assert.Contains(t, call.sql, "INSERT INTO outbox_entries")
	assert.Equal(t, "b2c3d4e5-f6a7-8901-bcde-f12345678901", call.args[0]) // id
	assert.Equal(t, "agg-2", call.args[1])                                 // aggregate_id
	assert.Equal(t, "order", call.args[2])                                  // aggregate_type
	assert.Equal(t, "order.shipped", call.args[3])                          // event_type
	assert.Equal(t, "", call.args[4])                                       // topic (empty string)

	// Verify metadata was serialized as JSON.
	metaJSON, ok := call.args[6].([]byte)
	require.True(t, ok)
	var meta map[string]string
	require.NoError(t, json.Unmarshal(metaJSON, &meta))
	assert.Equal(t, "test", meta["source"])
}

func TestOutboxWriter_Write_WithTopic(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:            "c3d4e5f6-a7b8-9012-cdef-123456789012",
		AggregateID:   "agg-t",
		AggregateType: "device",
		EventType:     "device.enrolled",
		Topic:         "custom.topic.v1",
		Payload:       []byte(`{"enrolled":true}`),
		CreatedAt:     time.Now(),
	}

	err := w.Write(ctx, entry)
	require.NoError(t, err)

	require.Len(t, tx.execCalls, 1)
	call := tx.execCalls[0]
	assert.Equal(t, "custom.topic.v1", call.args[4]) // topic column
}

func TestOutboxWriter_Write_InjectsObservabilityMetadataFromContext(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	ctx = ctxkeys.WithRequestID(ctx, "req-123")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-123")
	ctx = ctxkeys.WithTraceID(ctx, "trace-123")

	entry := outbox.Entry{
		ID:        "ctx-meta-0001",
		EventType: "order.created",
		Payload:   []byte(`{"id":"1"}`),
		CreatedAt: time.Now(),
		Metadata:  map[string]string{"source": "handler"},
	}

	err := w.Write(ctx, entry)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1)

	metaJSON, ok := tx.execCalls[0].args[6].([]byte)
	require.True(t, ok)

	var meta map[string]string
	require.NoError(t, json.Unmarshal(metaJSON, &meta))
	assert.Equal(t, "handler", meta["source"])
	assert.Equal(t, "req-123", meta["request_id"])
	assert.Equal(t, "corr-123", meta["correlation_id"])
	assert.Equal(t, "trace-123", meta["trace_id"])
}

func TestOutboxWriter_Write_ZeroCreatedAt(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:        "d4e5f6a7-b8c9-0123-defa-234567890123",
		EventType: "test.event",
		Payload:   []byte("{}"),
		// CreatedAt is zero
	}

	err := w.Write(ctx, entry)
	require.NoError(t, err)

	call := tx.execCalls[0]
	ts, ok := call.args[7].(time.Time)
	require.True(t, ok)
	assert.False(t, ts.IsZero(), "should default to now when CreatedAt is zero")
}

func TestOutboxWriter_Write_TxExecError(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{execErr: errcode.New(ErrAdapterPGQuery, "exec failed")}

	ctx := CtxWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:        "e5f6a7b8-c9d0-1234-efab-345678901234",
		EventType: "test",
		Payload:   []byte("{}"),
		CreatedAt: time.Now(),
	}

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGQuery, ec.Code)
}

func TestOutboxWriter_Write_EmptyID(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:        "",
		EventType: "test.event",
		Payload:   []byte("{}"),
		CreatedAt: time.Now(),
	}

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "must not be empty")
}

func TestOutboxWriter_Write_InvalidID(t *testing.T) {
	w := NewOutboxWriter()
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
			entry := outbox.Entry{
				ID:        tt.id,
				EventType: "test.event",
				Payload:   []byte("{}"),
				CreatedAt: time.Now(),
			}

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
	w := NewOutboxWriter()
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
			entry := outbox.Entry{
				ID:        tt.id,
				EventType: "test.event",
				Payload:   []byte("{}"),
				CreatedAt: time.Now(),
			}

			err := w.Write(ctx, entry)
			require.NoError(t, err)
			require.Len(t, tx.execCalls, 1)
			assert.Equal(t, tt.id, tx.execCalls[0].args[0])
		})
	}
}

func TestOutboxWriter_Write_MissingTopic(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	entry := outbox.Entry{
		ID:      "f6a7b8c9-d0e1-2345-faba-456789012345",
		Payload: []byte(`{"data":true}`),
		// Topic and EventType are both empty → Validate should fail
	}

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "topic")
	assert.Empty(t, tx.execCalls, "should not reach DB insert")
}

func TestOutboxWriter_Write_MissingPayload(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	entry := outbox.Entry{
		ID:    "a7b8c9d0-e1f2-3456-abcd-567890123456",
		Topic: "some.topic",
		// Payload is nil → Validate should fail
	}

	err := w.Write(ctx, entry)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "payload")
	assert.Empty(t, tx.execCalls, "should not reach DB insert")
}

// --- WriteBatch Tests ---

func TestOutboxWriter_WriteBatch_EmptySlice(t *testing.T) {
	w := NewOutboxWriter()
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
	w := NewOutboxWriter()
	entries := []outbox.Entry{{
		ID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Topic: "t", Payload: []byte("{}"),
	}}

	err := w.WriteBatch(context.Background(), entries)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGNoTx, ec.Code)
}

func TestOutboxWriter_WriteBatch_Success(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	entries := []outbox.Entry{
		{ID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Topic: "t1", Payload: []byte(`{"a":1}`), CreatedAt: time.Now()},
		{ID: "b2c3d4e5-f6a7-8901-bcde-f12345678901", Topic: "t2", Payload: []byte(`{"b":2}`), CreatedAt: time.Now()},
	}

	err := w.WriteBatch(ctx, entries)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1)

	call := tx.execCalls[0]
	assert.Contains(t, call.sql, "INSERT INTO outbox_entries")
	// 2 entries × 9 cols = 18 args
	assert.Len(t, call.args, 18)
	assert.Equal(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890", call.args[0])
	assert.Equal(t, "b2c3d4e5-f6a7-8901-bcde-f12345678901", call.args[9])
}

func TestOutboxWriter_WriteBatch_InjectsObservabilityMetadataFromContext(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}

	ctx := CtxWithTx(context.Background(), tx)
	ctx = ctxkeys.WithRequestID(ctx, "req-batch")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-batch")
	ctx = ctxkeys.WithTraceID(ctx, "trace-batch")

	entries := []outbox.Entry{
		{
			ID:        "batch-ctx-0001",
			Topic:     "orders.v1",
			Payload:   []byte(`{"idx":1}`),
			CreatedAt: time.Now(),
		},
		{
			ID:        "batch-ctx-0002",
			Topic:     "orders.v1",
			Payload:   []byte(`{"idx":2}`),
			CreatedAt: time.Now(),
			Metadata: map[string]string{
				"request_id": "req-explicit",
			},
		},
	}

	err := w.WriteBatch(ctx, entries)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 1)

	firstMetaJSON, ok := tx.execCalls[0].args[6].([]byte)
	require.True(t, ok)
	var firstMeta map[string]string
	require.NoError(t, json.Unmarshal(firstMetaJSON, &firstMeta))
	assert.Equal(t, "req-batch", firstMeta["request_id"])
	assert.Equal(t, "corr-batch", firstMeta["correlation_id"])
	assert.Equal(t, "trace-batch", firstMeta["trace_id"])

	secondMetaJSON, ok := tx.execCalls[0].args[15].([]byte)
	require.True(t, ok)
	var secondMeta map[string]string
	require.NoError(t, json.Unmarshal(secondMetaJSON, &secondMeta))
	assert.Equal(t, "req-explicit", secondMeta["request_id"])
	assert.Equal(t, "corr-batch", secondMeta["correlation_id"])
	assert.Equal(t, "trace-batch", secondMeta["trace_id"])
}

func TestOutboxWriter_WriteBatch_InvalidEntry(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	t.Run("empty ID", func(t *testing.T) {
		entries := []outbox.Entry{
			{ID: "valid-id", Topic: "t", Payload: []byte("{}")},
			{ID: "", Topic: "t", Payload: []byte("{}")},
		}
		err := w.WriteBatch(ctx, entries)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entry[1]")
		assert.Empty(t, tx.execCalls)
	})

	t.Run("all-zeros UUID", func(t *testing.T) {
		entries := []outbox.Entry{
			{ID: "valid-id", Topic: "t", Payload: []byte("{}")},
			{ID: "00000000-0000-0000-0000-000000000000", Topic: "t", Payload: []byte("{}")},
		}
		err := w.WriteBatch(ctx, entries)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entry[1]")
		assert.Contains(t, err.Error(), "all-zeros")
		assert.Empty(t, tx.execCalls)
	})
}

func TestOutboxWriter_WriteBatch_ExecError(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{execErr: errcode.New(ErrAdapterPGQuery, "batch exec failed")}
	ctx := CtxWithTx(context.Background(), tx)

	entries := []outbox.Entry{
		{ID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Topic: "t", Payload: []byte("{}"), CreatedAt: time.Now()},
	}

	err := w.WriteBatch(ctx, entries)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterPGQuery, ec.Code)
}

func TestOutboxWriter_WriteBatch_ChunksLargeBatch(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockOutboxTx{}
	ctx := CtxWithTx(context.Background(), tx)

	// Create writeBatchChunkSize + 1 entries to force 2 chunks.
	n := writeBatchChunkSize + 1
	entries := make([]outbox.Entry, n)
	for i := range n {
		entries[i] = outbox.Entry{
			ID:        fmt.Sprintf("evt-%012d", i),
			Topic:     "t",
			Payload:   []byte("{}"),
			CreatedAt: time.Now(),
		}
	}

	err := w.WriteBatch(ctx, entries)
	require.NoError(t, err)
	require.Len(t, tx.execCalls, 2, "should split into 2 chunks")
	assert.Len(t, tx.execCalls[0].args, writeBatchChunkSize*9)
	assert.Len(t, tx.execCalls[1].args, 1*9)
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
