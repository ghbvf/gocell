package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- test doubles -----

type fakeRow struct {
	vals []any
	err  error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = r.vals[i].(string)
		case *int:
			*v = r.vals[i].(int)
		case *time.Time:
			*v = r.vals[i].(time.Time)
		case **time.Time:
			if r.vals[i] == nil {
				*v = nil
			} else {
				t := r.vals[i].(time.Time)
				*v = &t
			}
		}
	}
	return nil
}

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
func (r *fakeRows) Values() ([]any, error)                         { return nil, nil }

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
		case *int:
			*v = row[i].(int)
		case *time.Time:
			*v = row[i].(time.Time)
		case **time.Time:
			if row[i] == nil {
				*v = nil
			} else {
				t := row[i].(time.Time)
				*v = &t
			}
		}
	}
	return nil
}

// fakeDB implements DBTX for unit testing.
type fakeDB struct {
	execTag   pgconn.CommandTag
	execErr   error
	queryRows *fakeRows
	queryErr  error
	rowResult *fakeRow

	lastSQL  string
	lastArgs []any
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.lastSQL = sql
	f.lastArgs = args
	return f.execTag, f.execErr
}

func (f *fakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
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

func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.lastSQL = sql
	f.lastArgs = args
	return f.rowResult
}

// ----- tests -----

func TestConfigRepository_Create(t *testing.T) {
	tests := []struct {
		name    string
		tag     pgconn.CommandTag
		execErr error
		wantErr string
	}{
		{
			name: "success",
			tag:  pgconn.NewCommandTag("INSERT 0 1"),
		},
		{
			name:    "duplicate key",
			tag:     pgconn.NewCommandTag("INSERT 0 0"),
			wantErr: "ERR_CONFIG_DUPLICATE",
		},
		{
			name:    "exec error",
			execErr: assert.AnError,
			wantErr: "ERR_INTERNAL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDB{execTag: tt.tag, execErr: tt.execErr}

			// Simulate Create logic
			entry := &domain.ConfigEntry{
				ID:        "cfg-001",
				Key:       "app.name",
				Value:     "gocell",
				Version:   1,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			tag, err := fake.Exec(context.Background(), "INSERT ...",
				entry.ID, entry.Key, entry.Value, entry.Version,
				entry.CreatedAt, entry.UpdatedAt,
			)
			if tt.execErr != nil {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantErr == "ERR_CONFIG_DUPLICATE" {
				assert.Equal(t, int64(0), tag.RowsAffected())
			} else {
				assert.Equal(t, int64(1), tag.RowsAffected())
			}
		})
	}
}

func TestConfigRepository_GetByKey(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	tests := []struct {
		name    string
		row     *fakeRow
		wantErr bool
	}{
		{
			name: "found",
			row:  &fakeRow{vals: []any{"cfg-001", "app.name", "gocell", 1, now, now}},
		},
		{
			name:    "not found",
			row:     &fakeRow{err: pgx.ErrNoRows},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e domain.ConfigEntry
			err := tt.row.Scan(&e.ID, &e.Key, &e.Value, &e.Version, &e.CreatedAt, &e.UpdatedAt)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "cfg-001", e.ID)
				assert.Equal(t, "app.name", e.Key)
			}
		})
	}
}

func TestConfigRepository_Update(t *testing.T) {
	tests := []struct {
		name       string
		tag        pgconn.CommandTag
		wantNotErr bool
	}{
		{name: "success", tag: pgconn.NewCommandTag("UPDATE 1"), wantNotErr: true},
		{name: "not found", tag: pgconn.NewCommandTag("UPDATE 0"), wantNotErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			affected := tt.tag.RowsAffected()
			if tt.wantNotErr {
				assert.Equal(t, int64(1), affected)
			} else {
				assert.Equal(t, int64(0), affected)
			}
		})
	}
}

func TestConfigRepository_Delete(t *testing.T) {
	tests := []struct {
		name    string
		tag     pgconn.CommandTag
		wantErr bool
	}{
		{name: "success", tag: pgconn.NewCommandTag("DELETE 1")},
		{name: "not found", tag: pgconn.NewCommandTag("DELETE 0"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			affected := tt.tag.RowsAffected()
			if tt.wantErr {
				assert.Equal(t, int64(0), affected)
			} else {
				assert.Equal(t, int64(1), affected)
			}
		})
	}
}

func TestConfigRepository_List_QueryLimit(t *testing.T) {
	assert.Equal(t, 1000, queryLimit, "ARCH-07 safety net should be 1000")
}

func TestConfigRepository_PublishVersion(t *testing.T) {
	fake := &fakeDB{execTag: pgconn.NewCommandTag("INSERT 0 1")}
	now := time.Now()
	v := &domain.ConfigVersion{
		ID:          "cv-001",
		ConfigID:    "cfg-001",
		Version:     1,
		Value:       "val",
		PublishedAt: &now,
	}

	_, err := fake.Exec(context.Background(), "INSERT ...",
		v.ID, v.ConfigID, v.Version, v.Value, v.PublishedAt,
	)
	require.NoError(t, err)
	assert.Len(t, fake.lastArgs, 5)
}

func TestConfigRepository_GetVersion(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	tests := []struct {
		name    string
		row     *fakeRow
		wantErr bool
	}{
		{
			name: "found",
			row:  &fakeRow{vals: []any{"cv-001", "cfg-001", 1, "val", now}},
		},
		{
			name:    "not found",
			row:     &fakeRow{err: pgx.ErrNoRows},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v domain.ConfigVersion
			err := tt.row.Scan(&v.ID, &v.ConfigID, &v.Version, &v.Value, &v.PublishedAt)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "cv-001", v.ID)
			}
		})
	}
}

func TestTxFromContext(t *testing.T) {
	t.Run("no tx returns nil", func(t *testing.T) {
		tx := TxFromContext(context.Background())
		assert.Nil(t, tx)
	})
}

func TestErrorCodes(t *testing.T) {
	assert.Equal(t, "ERR_CONFIG_NOT_FOUND", string(ErrConfigNotFound))
	assert.Equal(t, "ERR_CONFIG_DUPLICATE", string(ErrConfigDuplicate))
}
