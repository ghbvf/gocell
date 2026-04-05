package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxWriter_Write_NoTx(t *testing.T) {
	w := NewOutboxWriter()
	entry := outbox.Entry{
		ID:            "e-1",
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
	tx := &mockTx{}

	ctx := ContextWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:            "e-2",
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
	assert.Equal(t, "e-2", call.args[0])
	assert.Equal(t, "agg-2", call.args[1])
	assert.Equal(t, "order", call.args[2])
	assert.Equal(t, "order.shipped", call.args[3])

	// Verify metadata was serialized as JSON.
	metaJSON, ok := call.args[5].([]byte)
	require.True(t, ok)
	var meta map[string]string
	require.NoError(t, json.Unmarshal(metaJSON, &meta))
	assert.Equal(t, "test", meta["source"])
}

func TestOutboxWriter_Write_ZeroCreatedAt(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockTx{}

	ctx := ContextWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:        "e-3",
		EventType: "test.event",
		Payload:   []byte("{}"),
		// CreatedAt is zero
	}

	err := w.Write(ctx, entry)
	require.NoError(t, err)

	call := tx.execCalls[0]
	ts, ok := call.args[6].(time.Time)
	require.True(t, ok)
	assert.False(t, ts.IsZero(), "should default to now when CreatedAt is zero")
}

func TestOutboxWriter_Write_TxExecError(t *testing.T) {
	w := NewOutboxWriter()
	tx := &mockTx{execErr: errcode.New(ErrAdapterPGQuery, "exec failed")}

	ctx := ContextWithTx(context.Background(), tx)
	entry := outbox.Entry{
		ID:        "e-4",
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

// mockTx records exec calls for assertion.
type mockTx struct {
	execCalls []execCall
	execErr   error
}

type execCall struct {
	sql  string
	args []any
}

func (m *mockTx) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	m.execCalls = append(m.execCalls, execCall{sql: sql, args: args})
	if m.execErr != nil {
		return 0, m.execErr
	}
	return 1, nil
}

func (m *mockTx) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return nil, nil
}

func (m *mockTx) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return nil
}

func (m *mockTx) Commit(_ context.Context) error   { return nil }
func (m *mockTx) Rollback(_ context.Context) error { return nil }
