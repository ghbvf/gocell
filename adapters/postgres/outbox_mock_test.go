package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Test helpers for outbox_store_test.go
// ---------------------------------------------------------------------------

// makeRelayEntry constructs an outboxrt.ClaimedEntry for use in mock DB rows.
// It replaces the old makeRelayEntry(relayEntry) from outbox_relay_test.go.
func makeRelayEntry(id, eventType string, attempts int) outboxrt.ClaimedEntry {
	return outboxrt.ClaimedEntry{
		Entry: kout.Entry{
			ID:            id,
			AggregateID:   "agg-" + id,
			AggregateType: "test",
			EventType:     eventType,
			Payload:       []byte(`{"id":"` + id + `"}`),
			CreatedAt:     time.Now(),
		},
		Attempts: attempts,
	}
}

// makeMockRowData converts a ClaimedEntry into a mockRowData row for mockRows.
func makeMockRowData(e outboxrt.ClaimedEntry) mockRowData {
	metaJSON, _ := json.Marshal(e.Metadata)
	if e.Metadata == nil {
		metaJSON = []byte("null")
	}
	return mockRowData{
		values: []any{
			e.ID, e.AggregateID, e.AggregateType, e.EventType,
			e.Topic, e.Payload, metaJSON, e.CreatedAt, e.Attempts,
		},
	}
}

// ---------------------------------------------------------------------------
// mockDBTX — in-memory relayDB for unit tests
// ---------------------------------------------------------------------------

// execCall is defined in outbox_writer_test.go and shared across test files.

type queryCall struct {
	sql  string
	args []any
}

type mockDBTX struct {
	mu           sync.Mutex
	queryRows    *mockRows
	queryRowFn   func(sql string, args ...any) pgx.Row
	queryCalls   []queryCall
	queryRowSQLs []queryCall
	execCalls    []execCall
	execErr      error
	execResult   pgconn.CommandTag
	commitErr    error
	beginErr     error
}

func (m *mockDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCalls = append(m.execCalls, execCall{sql: sql, args: args})
	if m.execErr != nil {
		return pgconn.NewCommandTag(""), m.execErr
	}
	if m.execResult.String() != "" {
		return m.execResult, nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (m *mockDBTX) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queryCalls = append(m.queryCalls, queryCall{sql: sql, args: args})
	if m.queryRows == nil {
		return &mockRows{}, nil
	}
	return m.queryRows, nil
}

func (m *mockDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	m.mu.Lock()
	m.queryRowSQLs = append(m.queryRowSQLs, queryCall{sql: sql, args: args})
	fn := m.queryRowFn
	m.mu.Unlock()
	if fn != nil {
		return fn(sql, args...)
	}
	return &mockNullTimeRow{}
}

func (m *mockDBTX) Begin(_ context.Context) (pgx.Tx, error) {
	if m.beginErr != nil {
		return nil, m.beginErr
	}
	return &mockRelayTx{db: m}, nil
}

type mockRelayTx struct {
	db *mockDBTX
}

func (t *mockRelayTx) Begin(_ context.Context) (pgx.Tx, error) { return t, nil }
func (t *mockRelayTx) Commit(_ context.Context) error {
	if t.db.commitErr != nil {
		return t.db.commitErr
	}
	return nil
}
func (t *mockRelayTx) Rollback(_ context.Context) error { return nil }
func (t *mockRelayTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *mockRelayTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults { return nil }
func (t *mockRelayTx) LargeObjects() pgx.LargeObjects                             { return pgx.LargeObjects{} }
func (t *mockRelayTx) Prepare(_ context.Context, _ string, _ string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *mockRelayTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(ctx, sql, args...)
}
func (t *mockRelayTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.db.Query(ctx, sql, args...)
}
func (t *mockRelayTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return nil }
func (t *mockRelayTx) Conn() *pgx.Conn                                        { return nil }

// ---------------------------------------------------------------------------
// mockRows — in-memory pgx.Rows for unit tests
// ---------------------------------------------------------------------------

// mockNullTimeRow is the default pgx.Row returned by mockDBTX.QueryRow when no
// queryRowFn is set. It scans NULL into a *time.Time destination, which models
// "no rows found" for OldestEligibleAt unit tests that don't care about the
// QueryRow path.
type mockNullTimeRow struct{}

func (mockNullTimeRow) Scan(dest ...any) error {
	for _, d := range dest {
		if pp, ok := d.(**time.Time); ok {
			*pp = nil
		}
	}
	return nil
}

type mockRowData struct {
	values []any
}

type mockRows struct {
	entries []mockRowData
	idx     int
}

func (r *mockRows) Next() bool {
	return r.idx < len(r.entries)
}

func (r *mockRows) Scan(dest ...any) error {
	row := r.entries[r.idx]
	r.idx++
	if len(dest) != len(row.values) {
		return fmt.Errorf("mockRows.Scan: dest count %d != values count %d (S3-F4)", len(dest), len(row.values))
	}
	for i, v := range row.values {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
		case *int:
			*d = v.(int)
		default:
			return fmt.Errorf("mockRows.Scan: unsupported dest type %T at index %d", dest[i], i)
		}
	}
	return nil
}

func (r *mockRows) Close()                                       {}
func (r *mockRows) Err() error                                   { return nil }
func (r *mockRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("") }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *mockRows) Values() ([]any, error)                       { return nil, nil }
func (r *mockRows) RawValues() [][]byte                          { return nil }
func (r *mockRows) Conn() *pgx.Conn                              { return nil }

// ---------------------------------------------------------------------------
// mockDBTXIterErr — relayDB whose Query returns rows with a rows.Err()
// ---------------------------------------------------------------------------

type mockRowsWithIterErr struct {
	*mockRows
	iterErr error
}

func (r *mockRowsWithIterErr) Err() error { return r.iterErr }

type mockDBTXIterErr struct {
	iterErr error
}

func (m *mockDBTXIterErr) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &mockNullTimeRow{}
}

func (m *mockDBTXIterErr) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 0"), nil
}

func (m *mockDBTXIterErr) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return &mockRowsWithIterErr{
		mockRows: &mockRows{entries: nil},
		iterErr:  m.iterErr,
	}, nil
}

func (m *mockDBTXIterErr) Begin(_ context.Context) (pgx.Tx, error) {
	return &mockRelayTxIterErr{db: m}, nil
}

type mockRelayTxIterErr struct {
	db *mockDBTXIterErr
}

func (t *mockRelayTxIterErr) Begin(_ context.Context) (pgx.Tx, error) { return t, nil }
func (t *mockRelayTxIterErr) Commit(_ context.Context) error          { return nil }
func (t *mockRelayTxIterErr) Rollback(_ context.Context) error        { return nil }
func (t *mockRelayTxIterErr) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *mockRelayTxIterErr) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults { return nil }
func (t *mockRelayTxIterErr) LargeObjects() pgx.LargeObjects                             { return pgx.LargeObjects{} }
func (t *mockRelayTxIterErr) Prepare(_ context.Context, _ string, _ string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *mockRelayTxIterErr) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(ctx, sql, args...)
}
func (t *mockRelayTxIterErr) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.db.Query(ctx, sql, args...)
}
func (t *mockRelayTxIterErr) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return nil }
func (t *mockRelayTxIterErr) Conn() *pgx.Conn                                        { return nil }
