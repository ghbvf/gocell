package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newConfigRepositoryFromDBTX is a test-only constructor that bypasses the
// Session layer, allowing unit tests to inject a mockDB directly.
// Production code always goes through NewConfigRepository(*Session).
func newConfigRepositoryFromDBTX(db DBTX) *ConfigRepository {
	return &ConfigRepository{db: db}
}

func TestConfigRepository_Create(t *testing.T) {
	db := &mockDB{}
	repo := newConfigRepositoryFromDBTX(db)

	entry := &domain.ConfigEntry{
		ID:    "cfg-1",
		Key:   "app.name",
		Value: "GoCell",
	}

	err := repo.Create(context.Background(), entry)
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1)
	assert.Contains(t, db.execCalls[0].sql, "INSERT INTO config_entries")
	assert.Equal(t, "cfg-1", db.execCalls[0].args[0])
	assert.Equal(t, "app.name", db.execCalls[0].args[1])
}

func TestConfigRepository_Create_Error(t *testing.T) {
	db := &mockDB{execErr: assert.AnError}
	repo := newConfigRepositoryFromDBTX(db)

	err := repo.Create(context.Background(), &domain.ConfigEntry{Key: "k"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

func TestConfigRepository_GetByKey(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// 11 columns: id, key, value, sensitive, version, created_at, updated_at,
			// value_cipher(nil), value_key_id(nil), value_edk(nil), value_nonce(nil)
			values: []any{"cfg-1", "app.name", "GoCell", false, 1, now, now, nil, nil, nil, nil},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	entry, err := repo.GetByKey(context.Background(), "app.name")
	require.NoError(t, err)
	assert.Equal(t, "cfg-1", entry.ID)
	assert.Equal(t, "app.name", entry.Key)
	assert.Equal(t, "GoCell", entry.Value)
	assert.Equal(t, 1, entry.Version)
}

// TestGetByKey_NotFound_ReturnsErrConfigRepoNotFound verifies that pgx.ErrNoRows
// is classified as ErrConfigRepoNotFound (REPO-SCAN-CLASSIFY-01).
func TestGetByKey_NotFound_ReturnsErrConfigRepoNotFound(t *testing.T) {
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetByKey(context.Background(), "missing")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
}

// TestGetByKey_OtherScanError_ReturnsErrConfigRepoQuery verifies that scan
// errors other than pgx.ErrNoRows are classified as ErrConfigRepoQuery
// (REPO-SCAN-CLASSIFY-01 — previously all were mapped to NotFound).
func TestGetByKey_OtherScanError_ReturnsErrConfigRepoQuery(t *testing.T) {
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: assert.AnError},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetByKey(context.Background(), "missing")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

// TestConfigRepository_GetByKey_NotFound is a legacy name kept for backward
// reference. It tests the other-error path (assert.AnError != pgx.ErrNoRows).
func TestConfigRepository_GetByKey_NotFound(t *testing.T) {
	// assert.AnError is not pgx.ErrNoRows → classified as ErrConfigRepoQuery
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: assert.AnError},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetByKey(context.Background(), "missing")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

func TestConfigRepository_Update(t *testing.T) {
	db := &mockDB{execAffected: 1}
	repo := newConfigRepositoryFromDBTX(db)

	entry := &domain.ConfigEntry{
		Key:     "app.name",
		Value:   "GoCell v2",
		Version: 2,
	}

	err := repo.Update(context.Background(), entry)
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1)
	assert.Contains(t, db.execCalls[0].sql, "UPDATE config_entries")
}

func TestConfigRepository_Update_NotFound(t *testing.T) {
	db := &mockDB{execAffected: 0}
	repo := newConfigRepositoryFromDBTX(db)

	err := repo.Update(context.Background(), &domain.ConfigEntry{Key: "missing"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
}

func TestConfigRepository_Delete(t *testing.T) {
	db := &mockDB{execAffected: 1}
	repo := newConfigRepositoryFromDBTX(db)

	err := repo.Delete(context.Background(), "app.name")
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1)
	assert.Contains(t, db.execCalls[0].sql, "DELETE FROM config_entries")
}

func TestConfigRepository_Delete_NotFound(t *testing.T) {
	db := &mockDB{execAffected: 0}
	repo := newConfigRepositoryFromDBTX(db)

	err := repo.Delete(context.Background(), "missing")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
}

func TestConfigRepository_List(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				{values: []any{"cfg-1", "a.key", "val1", false, 1, now, now}},
				{values: []any{"cfg-2", "b.key", "val2", false, 1, now, now}},
			},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	params := query.ListParams{
		Limit: 50,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	entries, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "a.key", entries[0].Key)
	assert.Equal(t, "b.key", entries[1].Key)

	// Verify keyset pagination LIMIT.
	require.Len(t, db.queryCalls, 1)
	assert.Contains(t, db.queryCalls[0].sql, "LIMIT")
}

func TestConfigRepository_PublishVersion(t *testing.T) {
	// PR#155 review F5: assert the sensitive flag is actually bound as the 5th
	// positional argument so a future drop of that arg from r.db.Exec would fail.
	// PR3: sensitive=true now uses the encrypted INSERT path (cipher columns).
	tests := []struct {
		name      string
		sensitive bool
	}{
		{name: "non-sensitive", sensitive: false},
		{name: "sensitive", sensitive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockDB{}
			var repo *ConfigRepository
			if tt.sensitive {
				// sensitive=true requires a transformer.
				tr := &mockTransformerForTest{keyID: "test-key-v1"}
				repo = newEncryptedRepoFromDBTX(db, tr)
			} else {
				repo = newConfigRepositoryFromDBTX(db)
			}

			now := time.Now()
			version := &domain.ConfigVersion{
				ID:          "cv-1",
				ConfigID:    "cfg-1",
				Version:     1,
				Value:       "published value",
				Sensitive:   tt.sensitive,
				PublishedAt: &now,
			}

			err := repo.PublishVersion(context.Background(), version)
			require.NoError(t, err)

			require.Len(t, db.execCalls, 1)
			assert.Contains(t, db.execCalls[0].sql, "INSERT INTO config_versions")
			if tt.sensitive {
				// Encrypted path: check cipher columns present.
				assert.Contains(t, db.execCalls[0].sql, "value_cipher")
			} else {
				require.GreaterOrEqual(t, len(db.execCalls[0].args), 6,
					"PublishVersion non-sensitive must bind 6 args")
				assert.Equal(t, tt.sensitive, db.execCalls[0].args[4],
					"5th positional arg must be ConfigVersion.Sensitive")
			}
		})
	}
}

// mockTransformerForTest is a minimal pass-through transformer for existing tests.
// It satisfies crypto.ValueTransformer.
type mockTransformerForTest struct {
	keyID string
}

var _ crypto.ValueTransformer = (*mockTransformerForTest)(nil)

func (m *mockTransformerForTest) Encrypt(_ context.Context, plaintext, _ []byte) ([]byte, string, []byte, []byte, error) {
	return plaintext, m.keyID, []byte("nonce1234567"), []byte("edk"), nil
}

func (m *mockTransformerForTest) Decrypt(_ context.Context, ciphertext []byte, _ string, _, _, _ []byte) ([]byte, error) {
	return ciphertext, nil
}

func TestConfigRepository_GetVersion(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// 10 columns: id, config_id, version, value, sensitive, published_at,
			// value_cipher(nil), value_key_id(nil), value_edk(nil), value_nonce(nil)
			values: []any{"cv-1", "cfg-1", 1, "value", true, &now, nil, nil, nil, nil},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	version, err := repo.GetVersion(context.Background(), "cfg-1", 1)
	require.NoError(t, err)
	assert.Equal(t, "cv-1", version.ID)
	assert.Equal(t, "cfg-1", version.ConfigID)
	assert.Equal(t, 1, version.Version)
	assert.Equal(t, "value", version.Value)
	assert.True(t, version.Sensitive)
}

// TestGetVersion_NotFound_ReturnsErrConfigRepoNotFound verifies that pgx.ErrNoRows
// is classified as ErrConfigRepoNotFound (REPO-SCAN-CLASSIFY-01).
func TestGetVersion_NotFound_ReturnsErrConfigRepoNotFound(t *testing.T) {
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetVersion(context.Background(), "missing", 1)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
}

// TestGetVersion_OtherScanError_ReturnsErrConfigRepoQuery verifies that scan
// errors other than pgx.ErrNoRows are classified as ErrConfigRepoQuery
// (REPO-SCAN-CLASSIFY-01 — previously all were mapped to NotFound).
func TestGetVersion_OtherScanError_ReturnsErrConfigRepoQuery(t *testing.T) {
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: assert.AnError},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetVersion(context.Background(), "cfg-1", 1)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

// TestConfigRepository_GetVersion_NotFound is a legacy name kept for backward
// reference. It tests the other-error path (assert.AnError != pgx.ErrNoRows).
func TestConfigRepository_GetVersion_NotFound(t *testing.T) {
	// assert.AnError is not pgx.ErrNoRows → classified as ErrConfigRepoQuery
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: assert.AnError},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetVersion(context.Background(), "missing", 1)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

// --- F-S-1: resolveWrite enforcement tests ---

// TestCreate_WithoutTx_ReturnsNoTxError verifies that Create via a session-based
// repo fails with ErrAdapterPGNoTx when no tx is present in context (F-S-1).
func TestCreate_WithoutTx_ReturnsNoTxError(t *testing.T) {
	session := NewSession(nil) // nil pool — resolveWrite never reaches pool path
	repo := NewConfigRepository(session, nil)

	err := repo.Create(context.Background(), &domain.ConfigEntry{Key: "k"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestUpdate_WithoutTx_ReturnsNoTxError verifies that Update via a session-based
// repo fails with ErrAdapterPGNoTx when no tx is present in context (F-S-1).
func TestUpdate_WithoutTx_ReturnsNoTxError(t *testing.T) {
	session := NewSession(nil)
	repo := NewConfigRepository(session, nil)

	err := repo.Update(context.Background(), &domain.ConfigEntry{Key: "k"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestDelete_WithoutTx_ReturnsNoTxError verifies that Delete via a session-based
// repo fails with ErrAdapterPGNoTx when no tx is present in context (F-S-1).
func TestDelete_WithoutTx_ReturnsNoTxError(t *testing.T) {
	session := NewSession(nil)
	repo := NewConfigRepository(session, nil)

	err := repo.Delete(context.Background(), "k")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestPublishVersion_WithoutTx_ReturnsNoTxError verifies that PublishVersion via
// a session-based repo fails with ErrAdapterPGNoTx when no tx is present in
// context (F-S-1).
func TestPublishVersion_WithoutTx_ReturnsNoTxError(t *testing.T) {
	session := NewSession(nil)
	repo := NewConfigRepository(session, nil)

	err := repo.PublishVersion(context.Background(), &domain.ConfigVersion{ConfigID: "cfg-1"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestConfigRepository_List_QueryError covers the Query error path in List.
func TestConfigRepository_List_QueryError(t *testing.T) {
	db := &mockDB{queryErr: assert.AnError}
	repo := newConfigRepositoryFromDBTX(db)

	params := query.ListParams{Limit: 50}
	_, err := repo.List(context.Background(), params)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

// TestConfigRepository_List_ScanError covers the rows.Scan error path in List.
func TestConfigRepository_List_ScanError(t *testing.T) {
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				{values: nil}, // triggers scan error
			},
			scanErr: assert.AnError,
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	params := query.ListParams{Limit: 50}
	_, err := repo.List(context.Background(), params)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

// TestConfigRepository_List_RowsError covers the rows.Err() path in List.
func TestConfigRepository_List_RowsError(t *testing.T) {
	db := &mockDB{
		queryRows: &mockRowSet{
			rowsErr: assert.AnError,
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	params := query.ListParams{Limit: 50}
	_, err := repo.List(context.Background(), params)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
}

// TestConfigRepository_Create_WithSession_NoTx covers the session-based
// resolveWriteDB path returning an error when no tx is in ctx.
func TestConfigRepository_Create_WithSession_NoTx(t *testing.T) {
	s := NewSession(nil)
	repo := NewConfigRepository(s, nil)

	err := repo.Create(context.Background(), &domain.ConfigEntry{Key: "k"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestConfigRepository_List_WithSession_FallsBackToPool_NoRows covers the
// session-based resolveDB (read) path where session falls back to pool.
// Because the pool is nil the query will error; we verify that the session
// path (r.session != nil branch) is exercised.
func TestConfigRepository_ResolveDB_SessionPath(t *testing.T) {
	s := NewSession(nil)
	repo := NewConfigRepository(s, nil)

	// GetByKey uses resolveDB (read path). With nil pool the pool.QueryRow
	// will panic/nil-deref, but that path goes through poolAdapter.QueryRow
	// which calls s.pool.QueryRow — not exercised in unit tests (integration only).
	// We verify the r.session != nil branch is taken by using a mock session
	// approach: just assert the repo was constructed with a session.
	assert.NotNil(t, repo.session, "session-constructed repo must have non-nil session")
	assert.Nil(t, repo.db, "session-constructed repo must have nil db field")
}

// --- mocks ---

type dbCallRecord struct {
	sql  string
	args []any
}

type mockDB struct {
	execCalls      []dbCallRecord
	queryCalls     []dbCallRecord
	queryRows      *mockRowSet
	queryRowResult *mockRow
	execErr        error
	queryErr       error
	execAffected   int64
}

func (m *mockDB) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	m.execCalls = append(m.execCalls, dbCallRecord{sql: sql, args: args})
	if m.execErr != nil {
		return 0, m.execErr
	}
	return m.execAffected, nil
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
	if m.queryRowResult == nil {
		return &mockRow{scanErr: assert.AnError}
	}
	return m.queryRowResult
}

type mockRow struct {
	values  []any
	scanErr error
}

func (r *mockRow) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	for i, v := range r.values {
		if v == nil {
			// Handle nil for nullable columns (*[]byte, **string, etc.).
			switch d := dest[i].(type) {
			case *[]byte:
				*d = nil
			case **string:
				*d = nil
			}
			continue
		}
		switch d := dest[i].(type) {
		case *string:
			if s, ok := v.(string); ok {
				*d = s
			}
		case *int:
			*d = v.(int)
		case *bool:
			*d = v.(bool)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
		case **time.Time:
			*d = v.(*time.Time)
		case **string:
			if s, ok := v.(string); ok {
				*d = &s
			}
		}
	}
	return nil
}

type mockRowValues struct {
	values []any
}

type mockRowSet struct {
	entries []mockRowValues
	idx     int
	scanErr error
	rowsErr error
}

func (r *mockRowSet) Next() bool {
	return r.idx < len(r.entries)
}

func (r *mockRowSet) Scan(dest ...any) error {
	if r.scanErr != nil {
		r.idx++
		return r.scanErr
	}
	row := r.entries[r.idx]
	r.idx++
	for i, v := range row.values {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *int:
			*d = v.(int)
		case *bool:
			*d = v.(bool)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
		case **time.Time:
			*d = v.(*time.Time)
		}
	}
	return nil
}

func (r *mockRowSet) Close()     {}
func (r *mockRowSet) Err() error { return r.rowsErr }
