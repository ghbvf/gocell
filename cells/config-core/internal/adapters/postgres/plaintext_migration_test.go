package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// fakeDB for migration tests
// ---------------------------------------------------------------------------

// migrationRow represents a row in the fake DB.
type migrationRow struct {
	id        string
	key       string
	value     string
	cipher    []byte
	sensitive bool
}

// fakeDB implements DBTX for the migration tests. It supports:
// - Query: returns pending plaintext sensitive rows
// - Exec: records UPDATE calls
type fakeDB struct {
	rows      []migrationRow // all rows
	execCalls []fakeExecCall
	queryErr  error
	execErr   error
	execRowsN int64 // rows returned per Exec (0 → default 1)
}

type fakeExecCall struct {
	sql  string
	args []any
}

func (db *fakeDB) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	if db.execErr != nil {
		return 0, db.execErr
	}
	db.execCalls = append(db.execCalls, fakeExecCall{sql: sql, args: args})
	n := db.execRowsN
	if n == 0 {
		n = 1
	}
	return n, nil
}

func (db *fakeDB) Query(_ context.Context, _ string, args ...any) (Rows, error) {
	if db.queryErr != nil {
		return nil, db.queryErr
	}
	limit := 50
	if len(args) > 0 {
		if l, ok := args[0].(int); ok {
			limit = l
		}
	}
	// Return only rows with no cipher (pending encryption).
	var pending []migrationRow
	for _, r := range db.rows {
		if r.sensitive && len(r.cipher) == 0 {
			pending = append(pending, r)
		}
	}
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return &fakeRows{rows: pending}, nil
}

func (db *fakeDB) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return &fakeEmptyRow{}
}

// fakeRows implements Rows.
type fakeRows struct {
	rows []migrationRow
	pos  int
	err  error
}

func (r *fakeRows) Next() bool {
	if r.err != nil {
		return false
	}
	return r.pos < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.pos >= len(r.rows) {
		return errors.New("no rows")
	}
	row := r.rows[r.pos]
	r.pos++
	if len(dest) < 3 {
		return fmt.Errorf("scan: want 3 dest, got %d", len(dest))
	}
	*dest[0].(*string) = row.id
	*dest[1].(*string) = row.key
	*dest[2].(*string) = row.value
	return nil
}

func (r *fakeRows) Close()     {}
func (r *fakeRows) Err() error { return r.err }

// fakeEmptyRow implements Row — always returns ErrNoRows.
type fakeEmptyRow struct{}

func (r *fakeEmptyRow) Scan(_ ...any) error { return errors.New("no rows") }

// ---------------------------------------------------------------------------
// noopTransformerForMigTest — predictable encrypt: prepend "enc:" prefix
// ---------------------------------------------------------------------------

type noopTransformerForMigTest struct{}

func (noopTransformerForMigTest) Encrypt(_ context.Context, plaintext, _ []byte) ([]byte, string, []byte, []byte, error) {
	ct := append([]byte("enc:"), plaintext...)
	return ct, "test-key-v1", []byte("nonce"), []byte("edk"), nil
}

func (noopTransformerForMigTest) Decrypt(_ context.Context, ciphertext []byte, _ string, _, _, _ []byte) ([]byte, error) {
	return ciphertext[4:], nil // strip "enc:" prefix
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPlaintextMigrator_NilTransformer_Fails(t *testing.T) {
	_, err := newPlaintextMigrator(&fakeDB{}, nil, PlaintextMigrationConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transformer must not be nil")
}

func TestPlaintextMigrator_DefaultBatchSize(t *testing.T) {
	m, err := newPlaintextMigrator(&fakeDB{}, noopTransformerForMigTest{}, PlaintextMigrationConfig{BatchSize: 0})
	require.NoError(t, err)
	assert.Equal(t, 50, m.cfg.BatchSize)
}

func TestPlaintextMigrator_MigrateConfigEntries_Empty(t *testing.T) {
	db := &fakeDB{}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{BatchSize: 10})
	require.NoError(t, err)

	result, err := m.MigrateConfigEntries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result.Processed)
	assert.Equal(t, 0, len(db.execCalls))
}

func TestPlaintextMigrator_MigrateConfigEntries_SingleRow(t *testing.T) {
	db := &fakeDB{
		rows: []migrationRow{
			{id: "row-1", key: "db.password", value: "s3cret", sensitive: true},
		},
	}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{BatchSize: 10})
	require.NoError(t, err)

	result, err := m.MigrateConfigEntries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Processed)
	require.Len(t, db.execCalls, 1)

	// Check that UPDATE was called with the encrypted payload.
	call := db.execCalls[0]
	ct, ok := call.args[0].([]byte)
	require.True(t, ok, "arg[0] should be []byte ciphertext")
	assert.Equal(t, []byte("enc:s3cret"), ct)
	assert.Equal(t, "test-key-v1", call.args[1])
}

func TestPlaintextMigrator_MigrateConfigEntries_BatchProcessing(t *testing.T) {
	rows := make([]migrationRow, 5)
	for i := range rows {
		rows[i] = migrationRow{
			id:        fmt.Sprintf("row-%d", i),
			key:       fmt.Sprintf("key.%d", i),
			value:     fmt.Sprintf("val%d", i),
			sensitive: true,
		}
	}
	db := &fakeDB{rows: rows}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{BatchSize: 3})
	require.NoError(t, err)

	// fakeDB returns all pending each Query call regardless of prior Exec (no in-memory update).
	// Limit the fakeDB to only return up to BatchSize rows per call (already done in Query impl).
	// But successive calls keep returning the same rows since we don't update fakeDB state.
	// To avoid infinite loop in test, make the second query return empty.
	// We simulate idempotency by making Exec update the rows in memory.
	// For simplicity, override db.rows to shrink after each batch by wrapping.
	db2 := &batchFakeDB{rows: rows}
	m2, err := newPlaintextMigrator(db2, noopTransformerForMigTest{}, PlaintextMigrationConfig{BatchSize: 3})
	require.NoError(t, err)

	result, err := m2.MigrateConfigEntries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 5, result.Processed)
	_ = m // suppress unused warning
}

// batchFakeDB simulates DB state by removing rows from the pending list on Exec.
type batchFakeDB struct {
	rows      []migrationRow
	execCalls []fakeExecCall
}

func (db *batchFakeDB) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	// Mark the row with this ID as encrypted (remove from pending).
	if len(args) >= 5 {
		if id, ok := args[4].(string); ok {
			for i, r := range db.rows {
				if r.id == id {
					db.rows[i].cipher = []byte("encrypted")
					break
				}
			}
		}
	}
	db.execCalls = append(db.execCalls, fakeExecCall{sql: sql, args: args})
	return 1, nil
}

func (db *batchFakeDB) Query(_ context.Context, _ string, args ...any) (Rows, error) {
	limit := 50
	if len(args) > 0 {
		if l, ok := args[0].(int); ok {
			limit = l
		}
	}
	var pending []migrationRow
	for _, r := range db.rows {
		if r.sensitive && len(r.cipher) == 0 {
			pending = append(pending, r)
		}
	}
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return &fakeRows{rows: pending}, nil
}

func (db *batchFakeDB) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return &fakeEmptyRow{}
}

func TestPlaintextMigrator_QueryError_Propagates(t *testing.T) {
	db := &fakeDB{queryErr: errors.New("connection lost")}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{})
	require.NoError(t, err)

	_, err = m.MigrateConfigEntries(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

func TestPlaintextMigrator_ExecError_Propagates(t *testing.T) {
	db := &fakeDB{
		rows:    []migrationRow{{id: "r1", key: "k", value: "v", sensitive: true}},
		execErr: errors.New("update failed"),
	}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{})
	require.NoError(t, err)

	_, err = m.MigrateConfigEntries(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failed")
}

func TestPlaintextMigrator_Idempotent_AlreadyEncryptedRowsSkipped(t *testing.T) {
	// Rows with cipher already set should not be returned by the SELECT query.
	db := &fakeDB{
		rows: []migrationRow{
			{id: "r1", key: "k1", value: "", sensitive: true, cipher: []byte("already-enc")},
			{id: "r2", key: "k2", value: "plaintext", sensitive: true},
		},
	}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{})
	require.NoError(t, err)

	result, err := m.MigrateConfigEntries(context.Background())
	require.NoError(t, err)
	// Only r2 is pending.
	assert.Equal(t, 1, result.Processed)
	require.Len(t, db.execCalls, 1)
}

func TestPlaintextMigrator_NonSensitiveRowsSkipped(t *testing.T) {
	db := &fakeDB{
		rows: []migrationRow{
			{id: "r1", key: "k1", value: "open", sensitive: false},
		},
	}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{})
	require.NoError(t, err)

	result, err := m.MigrateConfigEntries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result.Processed)
	assert.Empty(t, db.execCalls)
}

func TestPlaintextMigrator_MigrateConfigVersions_SingleRow(t *testing.T) {
	db := &fakeDB{
		rows: []migrationRow{
			{id: "v1", key: "app.secret", value: "tok", sensitive: true},
		},
	}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{})
	require.NoError(t, err)

	result, err := m.MigrateConfigVersions(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Processed)
	require.Len(t, db.execCalls, 1)
}
