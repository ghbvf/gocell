package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExecResult implements sql.Result for test assertions.
type mockExecResult struct {
	lastID   int64
	affected int64
}

func (r mockExecResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r mockExecResult) RowsAffected() (int64, error) { return r.affected, nil }

// mockExecutor records ExecContext calls for assertion.
type mockExecutor struct {
	execCalls []mockExecCall
	execErr   error
}

type mockExecCall struct {
	query string
	args  []any
}

func (m *mockExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	m.execCalls = append(m.execCalls, mockExecCall{query: query, args: args})
	if m.execErr != nil {
		return nil, m.execErr
	}
	return mockExecResult{affected: 1}, nil
}

func (m *mockExecutor) QueryContext(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New("not implemented")
}

var _ Executor = (*mockExecutor)(nil)

func TestOutboxWriter_CompileCheck(t *testing.T) {
	var _ outbox.Writer = (*OutboxWriter)(nil)
}

func TestOutboxWriter_Write(t *testing.T) {
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)

	baseEntry := outbox.Entry{
		ID:            "e-001",
		AggregateID:   "agg-001",
		AggregateType: "order",
		EventType:     "order.created",
		Payload:       []byte(`{"amount":100}`),
		Metadata:      map[string]string{"source": "test", "trace_id": "t-123"},
		CreatedAt:     now,
	}

	tests := []struct {
		name       string
		ctx        func() context.Context
		entry      outbox.Entry
		execErr    error
		wantErr    bool
		wantCode   errcode.Code
		wantCalls  int
		checkArgs  func(t *testing.T, args []any)
	}{
		{
			name:     "no transaction in context returns ERR_ADAPTER_PG_NO_TX",
			ctx:      context.Background,
			entry:    baseEntry,
			wantErr:  true,
			wantCode: ErrAdapterPGNoTx,
		},
		{
			name: "successful write with all fields",
			ctx: func() context.Context {
				return contextWithExecutor(context.Background(), &mockExecutor{})
			},
			entry:     baseEntry,
			wantCalls: 1,
			checkArgs: func(t *testing.T, args []any) {
				t.Helper()
				require.Len(t, args, 7)
				assert.Equal(t, "e-001", args[0])
				assert.Equal(t, "agg-001", args[1])
				assert.Equal(t, "order", args[2])
				assert.Equal(t, "order.created", args[3])
				assert.Equal(t, []byte(`{"amount":100}`), args[4])
				// args[5] is metadata JSON - verify it's valid JSON containing expected keys
				metaJSON, ok := args[5].([]byte)
				require.True(t, ok)
				assert.Contains(t, string(metaJSON), `"source"`)
				assert.Contains(t, string(metaJSON), `"trace_id"`)
				assert.Equal(t, now, args[6])
			},
		},
		{
			name: "nil metadata marshals successfully",
			ctx: func() context.Context {
				return contextWithExecutor(context.Background(), &mockExecutor{})
			},
			entry: outbox.Entry{
				ID:            "e-002",
				AggregateID:   "agg-002",
				AggregateType: "user",
				EventType:     "user.created",
				Payload:       []byte(`{}`),
				Metadata:      nil,
				CreatedAt:     now,
			},
			wantCalls: 1,
			checkArgs: func(t *testing.T, args []any) {
				t.Helper()
				require.Len(t, args, 7)
				metaJSON, ok := args[5].([]byte)
				require.True(t, ok)
				assert.Equal(t, "null", string(metaJSON))
			},
		},
		{
			name: "exec failure returns ERR_ADAPTER_PG_QUERY",
			ctx: func() context.Context {
				return contextWithExecutor(context.Background(), &mockExecutor{
					execErr: errors.New("connection refused"),
				})
			},
			entry:    baseEntry,
			wantErr:  true,
			wantCode: ErrAdapterPGQuery,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewOutboxWriter(nil)
			ctx := tt.ctx()

			// Extract the mock executor if one was placed in context.
			var mock *mockExecutor
			if exec, ok := ExecutorFromContext(ctx); ok {
				mock, _ = exec.(*mockExecutor)
			}
			if tt.execErr != nil && mock != nil {
				mock.execErr = tt.execErr
			}

			err := w.Write(ctx, tt.entry)

			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr), "error should be *errcode.Error")
				assert.Equal(t, tt.wantCode, ecErr.Code)
				return
			}

			require.NoError(t, err)

			if mock != nil && tt.wantCalls > 0 {
				require.Len(t, mock.execCalls, tt.wantCalls)
				assert.Contains(t, mock.execCalls[0].query, "INSERT INTO outbox_entries")

				if tt.checkArgs != nil {
					tt.checkArgs(t, mock.execCalls[0].args)
				}
			}
		})
	}
}
