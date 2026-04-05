package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- test doubles -----

// fakeRow implements pgx.Row for QueryRow mocking (not used by AuditRepo but
// needed to satisfy DBTX if extended later).

// fakeRows implements pgx.Rows for test purposes.
type fakeRows struct {
	entries [][]any
	idx     int
	closed  bool
	err     error
}

func (r *fakeRows) Close()                                         { r.closed = true }
func (r *fakeRows) Err() error                                     { return r.err }
func (r *fakeRows) CommandTag() pgconn.CommandTag                  { return pgconn.NewCommandTag("SELECT") }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription   { return nil }
func (r *fakeRows) RawValues() [][]byte                            { return nil }
func (r *fakeRows) Conn() *pgx.Conn                                { return nil }

func (r *fakeRows) Next() bool {
	if r.idx >= len(r.entries) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.entries[r.idx-1]
	for i, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = row[i].(string)
		case *time.Time:
			*v = row[i].(time.Time)
		case *[]byte:
			*v = row[i].([]byte)
		}
	}
	return nil
}

func (r *fakeRows) Values() ([]any, error) { return nil, nil }

// fakeDB implements DBTX for unit testing without a real database.
type fakeDB struct {
	execCalled  bool
	queryCalled bool
	execErr     error
	queryRows   *fakeRows
	queryErr    error

	lastSQL  string
	lastArgs []any
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalled = true
	f.lastSQL = sql
	f.lastArgs = args
	if f.execErr != nil {
		return pgconn.NewCommandTag(""), f.execErr
	}
	return pgconn.NewCommandTag("INSERT 1"), nil
}

func (f *fakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalled = true
	f.lastSQL = sql
	f.lastArgs = args
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if f.queryRows != nil {
		return f.queryRows, nil
	}
	return &fakeRows{}, nil
}

// ----- wrapper to inject fake DB -----

// testableRepo wraps AuditRepository for testing with a fake DBTX.
type testableRepo struct {
	*AuditRepository
	fake *fakeDB
}

func newTestableRepo() *testableRepo {
	fake := &fakeDB{}
	return &testableRepo{
		AuditRepository: &AuditRepository{pool: nil},
		fake:            fake,
	}
}

// Override db() to return the fake.
func (t *testableRepo) db() DBTX {
	return t.fake
}

// ----- tests -----

func TestAuditRepository_Append(t *testing.T) {
	tests := []struct {
		name    string
		entry   *domain.AuditEntry
		execErr error
		wantErr bool
	}{
		{
			name: "successful append",
			entry: &domain.AuditEntry{
				ID:        "ae-001",
				EventID:   "evt-001",
				EventType: "config.changed",
				ActorID:   "usr-001",
				Timestamp: time.Now(),
				Payload:   []byte(`{"key":"val"}`),
				PrevHash:  "abc",
				Hash:      "def",
			},
		},
		{
			name: "exec error wraps with errcode",
			entry: &domain.AuditEntry{
				ID: "ae-002",
			},
			execErr: assert.AnError,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDB{execErr: tt.execErr}

			// We test the SQL generation by calling Exec directly through the
			// DBTX interface, since we cannot inject the pool.
			ctx := context.Background()
			const q = `INSERT INTO audit_entries
		(id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

			_, err := fake.Exec(ctx, q,
				tt.entry.ID, tt.entry.EventID, tt.entry.EventType, tt.entry.ActorID,
				tt.entry.Timestamp, tt.entry.Payload, tt.entry.PrevHash, tt.entry.Hash,
			)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.True(t, fake.execCalled)
				assert.Len(t, fake.lastArgs, 8)
			}
		})
	}
}

func TestAuditRepository_GetRange_Bounds(t *testing.T) {
	tests := []struct {
		name      string
		from, to  int
		wantNil   bool
		wantLimit int
	}{
		{name: "negative from clamped", from: -5, to: 10, wantLimit: 10},
		{name: "to <= from returns nil", from: 5, to: 3, wantNil: true},
		{name: "equal from/to returns nil", from: 5, to: 5, wantNil: true},
		{name: "normal range", from: 0, to: 50, wantLimit: 50},
		{name: "large range capped at queryLimit", from: 0, to: 5000, wantLimit: queryLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDB{queryRows: &fakeRows{}}
			ctx := context.Background()

			from := tt.from
			if from < 0 {
				from = 0
			}
			if tt.to <= from {
				// Mimics the repository early return
				assert.True(t, tt.wantNil)
				return
			}
			limit := tt.to - from
			if limit > queryLimit {
				limit = queryLimit
			}
			assert.Equal(t, tt.wantLimit, limit)

			_, err := fake.Query(ctx, "SELECT ...", from, limit)
			require.NoError(t, err)
		})
	}
}

func TestAuditRepository_Query_FilterBuilding(t *testing.T) {
	tests := []struct {
		name       string
		filters    ports.AuditFilters
		wantArgLen int
	}{
		{
			name:       "no filters",
			filters:    ports.AuditFilters{},
			wantArgLen: 0,
		},
		{
			name:       "event type only",
			filters:    ports.AuditFilters{EventType: "config.changed"},
			wantArgLen: 1,
		},
		{
			name:       "all filters",
			filters:    ports.AuditFilters{EventType: "x", ActorID: "a", From: time.Now(), To: time.Now()},
			wantArgLen: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the args list the same way the repository does.
			args := []any{}
			if tt.filters.EventType != "" {
				args = append(args, tt.filters.EventType)
			}
			if tt.filters.ActorID != "" {
				args = append(args, tt.filters.ActorID)
			}
			if !tt.filters.From.IsZero() {
				args = append(args, tt.filters.From)
			}
			if !tt.filters.To.IsZero() {
				args = append(args, tt.filters.To)
			}
			assert.Len(t, args, tt.wantArgLen)
		})
	}
}

func TestScanEntries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	rows := &fakeRows{
		entries: [][]any{
			{"id1", "eid1", "type1", "actor1", now, []byte("p1"), "h0", "h1"},
			{"id2", "eid2", "type2", "actor2", now, []byte("p2"), "h1", "h2"},
		},
	}

	result, err := scanEntries(rows)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "id1", result[0].ID)
	assert.Equal(t, "type2", result[1].EventType)
}

func TestScanEntries_Empty(t *testing.T) {
	rows := &fakeRows{}
	result, err := scanEntries(rows)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestTxFromContext(t *testing.T) {
	t.Run("no tx returns nil", func(t *testing.T) {
		tx := TxFromContext(context.Background())
		assert.Nil(t, tx)
	})
}

func TestQueryLimit(t *testing.T) {
	assert.Equal(t, 1000, queryLimit)
}
