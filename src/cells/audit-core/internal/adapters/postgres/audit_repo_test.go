package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditRepository_Append(t *testing.T) {
	db := &mockDB{}
	repo := NewAuditRepository(db)

	entry := &domain.AuditEntry{
		ID:        "ae-1",
		EventID:   "evt-1",
		EventType: "session.created",
		ActorID:   "usr-1",
		Timestamp: time.Now(),
		Payload:   []byte(`{"action":"login"}`),
		PrevHash:  "abc",
		Hash:      "def",
	}

	err := repo.Append(context.Background(), entry)
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1)
	call := db.execCalls[0]
	assert.Contains(t, call.sql, "INSERT INTO audit_entries")
	assert.Equal(t, "ae-1", call.args[0])
}

func TestAuditRepository_Append_ZeroTimestamp(t *testing.T) {
	db := &mockDB{}
	repo := NewAuditRepository(db)

	entry := &domain.AuditEntry{
		ID:        "ae-2",
		EventType: "test",
	}

	err := repo.Append(context.Background(), entry)
	require.NoError(t, err)

	ts, ok := db.execCalls[0].args[4].(time.Time)
	require.True(t, ok)
	assert.False(t, ts.IsZero())
}

func TestAuditRepository_Append_Error(t *testing.T) {
	db := &mockDB{execErr: assert.AnError}
	repo := NewAuditRepository(db)

	err := repo.Append(context.Background(), &domain.AuditEntry{ID: "ae-3"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuditRepoQuery, ec.Code)
}

func TestAuditRepository_GetRange(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				{values: []any{"ae-1", "evt-1", "session.created", "usr-1", now, []byte("{}"), "h0", "h1"}},
				{values: []any{"ae-2", "evt-2", "session.logout", "usr-2", now, []byte("{}"), "h1", "h2"}},
			},
		},
	}
	repo := NewAuditRepository(db)

	entries, err := repo.GetRange(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "ae-1", entries[0].ID)
	assert.Equal(t, "ae-2", entries[1].ID)
}

func TestAuditRepository_GetRange_Empty(t *testing.T) {
	db := &mockDB{queryRows: &mockRowSet{}}
	repo := NewAuditRepository(db)

	entries, err := repo.GetRange(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAuditRepository_GetRange_InvalidRange(t *testing.T) {
	db := &mockDB{}
	repo := NewAuditRepository(db)

	entries, err := repo.GetRange(context.Background(), 5, 2)
	require.NoError(t, err)
	assert.Nil(t, entries)
}

func TestAuditRepository_Query_WithFilters(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				{values: []any{"ae-1", "evt-1", "login", "usr-1", now, []byte("{}"), "", "h1"}},
			},
		},
	}
	repo := NewAuditRepository(db)

	filters := ports.AuditFilters{
		EventType: "login",
		ActorID:   "usr-1",
		From:      now.Add(-1 * time.Hour),
		To:        now,
	}

	params := query.ListParams{
		Limit: 50,
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: "DESC"},
			{Name: "id", Direction: "ASC"},
		},
	}

	entries, err := repo.Query(context.Background(), filters, params)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "ae-1", entries[0].ID)

	// Verify the query contained filter clauses and keyset ordering.
	require.Len(t, db.queryCalls, 1)
	assert.Contains(t, db.queryCalls[0].sql, "event_type = $1")
	assert.Contains(t, db.queryCalls[0].sql, "actor_id = $2")
	assert.Contains(t, db.queryCalls[0].sql, "timestamp >= $3")
	assert.Contains(t, db.queryCalls[0].sql, "timestamp <= $4")
	assert.Contains(t, db.queryCalls[0].sql, "ORDER BY timestamp DESC, id ASC")
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "10"},
		{42, "42"},
		{100, "100"},
		{1000, "1000"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, itoa(tt.input))
	}
}

// --- mocks ---

type dbCallRecord struct {
	sql  string
	args []any
}

type mockDB struct {
	execCalls  []dbCallRecord
	queryCalls []dbCallRecord
	queryRows  *mockRowSet
	execErr    error
	queryErr   error
}

func (m *mockDB) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	m.execCalls = append(m.execCalls, dbCallRecord{sql: sql, args: args})
	if m.execErr != nil {
		return 0, m.execErr
	}
	return 1, nil
}

func (m *mockDB) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	m.queryCalls = append(m.queryCalls, dbCallRecord{sql: sql, args: args})
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryRows == nil {
		return &mockRowSet{}, nil
	}
	return m.queryRows, nil
}

func (m *mockDB) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return nil
}

type mockRowValues struct {
	values []any
}

type mockRowSet struct {
	entries []mockRowValues
	idx     int
}

func (r *mockRowSet) Next() bool {
	return r.idx < len(r.entries)
}

func (r *mockRowSet) Scan(dest ...any) error {
	row := r.entries[r.idx]
	r.idx++
	for i, v := range row.values {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
		}
	}
	return nil
}

func (r *mockRowSet) Close() {}
func (r *mockRowSet) Err() error { return nil }
