package postgres

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	configcrypto "github.com/ghbvf/gocell/cells/config-core/internal/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// ---------------------------------------------------------------------------
// aadAwareTransformer — encodes AAD into the ciphertext so Decrypt can verify
// ---------------------------------------------------------------------------

// aadAwareTransformer is an in-memory transformer that embeds the AAD length
// and bytes at the head of the ciphertext. This allows Decrypt to assert that
// the caller supplied the identical AAD that was used during Encrypt, proving
// that the migration wires the correct AAD (cellID + configKey).
//
// Ciphertext layout: [4-byte big-endian AAD length][AAD bytes][plaintext bytes]
type aadAwareTransformer struct {
	keyID string
}

func (t *aadAwareTransformer) Encrypt(_ context.Context, pt, aad []byte) ([]byte, string, []byte, []byte, error) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(aad)))
	ct := make([]byte, 0, 4+len(aad)+len(pt))
	ct = append(ct, lenBuf[:]...)
	ct = append(ct, aad...)
	ct = append(ct, pt...)
	return ct, t.keyID, nil, nil, nil
}

func (t *aadAwareTransformer) Decrypt(_ context.Context, ct []byte, _ string, _, _, aad []byte) ([]byte, error) {
	if len(ct) < 4 {
		return nil, errors.New("cipher too short")
	}
	aadLen := binary.BigEndian.Uint32(ct[:4])
	if int(4+aadLen) > len(ct) {
		return nil, errors.New("cipher malformed")
	}
	storedAAD := ct[4 : 4+aadLen]
	if !bytes.Equal(storedAAD, aad) {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "aad mismatch")
	}
	return ct[4+aadLen:], nil
}

// ---------------------------------------------------------------------------
// TestPlaintextMigration_AADBinding
// ---------------------------------------------------------------------------

// TestPlaintextMigration_AADBinding verifies that the migrator passes the
// correct AAD (cellID + configKey) to the transformer, so the ciphertext is
// bound to the specific row identity and cannot be transplanted.
//
// Test flow:
//  1. Migrate a plaintext row → ciphertext stored in db.execCalls.
//  2. Extract the ciphertext and decrypt it with the CORRECT AAD → plaintext matches.
//  3. Attempt to decrypt the same ciphertext with a WRONG AAD → must fail with
//     ErrKeyProviderDecryptFailed (cross-row replay protection verified).
func TestPlaintextMigration_AADBinding(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		configKey string
		value     string
	}{
		{name: "api_key row", configKey: "api_key", value: "sk-secret"},
		{name: "db_password row", configKey: "db_password", value: "p@ssw0rd!"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &aadAwareTransformer{keyID: "test-key-v1"}
			db := &batchFakeDB{
				rows: []migrationRow{
					{id: "row-1", key: tc.configKey, value: tc.value, sensitive: true},
				},
			}
			m, err := newPlaintextMigrator(db, tr, PlaintextMigrationConfig{BatchSize: 10})
			require.NoError(t, err)

			result, err := m.MigrateConfigEntries(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, result.Processed)
			require.Len(t, db.execCalls, 1)

			// Extract the ciphertext written to the DB.
			ct, ok := db.execCalls[0].args[0].([]byte)
			require.True(t, ok, "first arg of UPDATE must be []byte ciphertext")

			// Correct AAD: must decrypt successfully and match original plaintext.
			correctAAD := configcrypto.AADForConfig(cellID, tc.configKey)
			pt, err := tr.Decrypt(ctx, ct, "test-key-v1", nil, nil, correctAAD)
			require.NoError(t, err, "Decrypt with correct AAD must succeed")
			assert.Equal(t, tc.value, string(pt), "decrypted plaintext must match original")

			// Wrong AAD (different configKey): must fail — cross-row replay blocked.
			wrongAAD := configcrypto.AADForConfig(cellID, "other_key")
			_, err = tr.Decrypt(ctx, ct, "test-key-v1", nil, nil, wrongAAD)
			require.Error(t, err, "Decrypt with wrong AAD must fail")
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "error must be errcode.Error")
			assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// TestPlaintextMigrator_CAS_ConcurrentWrite_Skipped (B1)
// ---------------------------------------------------------------------------

// casFakeDB simulates a DB where the CAS predicate (value_cipher IS NULL) fails
// because a concurrent writer already encrypted the row. Exec returns 0 rows
// affected instead of 1.
type casFakeDB struct {
	rows      []migrationRow
	execCalls []fakeExecCall
}

func (db *casFakeDB) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	db.execCalls = append(db.execCalls, fakeExecCall{sql: sql, args: args})
	return 0, nil // CAS predicate failed: row was already encrypted
}

func (db *casFakeDB) Query(_ context.Context, _ string, args ...any) (Rows, error) {
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

func (db *casFakeDB) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return &fakeEmptyRow{}
}

// TestPlaintextMigrator_CAS_ConcurrentWrite_Skipped verifies that when the UPDATE
// CAS predicate (value_cipher IS NULL) matches 0 rows — because a concurrent
// writer already encrypted the row between our SELECT and UPDATE — the migrator
// increments Skipped (not Processed) and returns no error.
func TestPlaintextMigrator_CAS_ConcurrentWrite_Skipped(t *testing.T) {
	db := &casFakeDB{
		rows: []migrationRow{
			{id: "r1", key: "db.password", value: "s3cret", sensitive: true},
		},
	}
	m, err := newPlaintextMigrator(db, noopTransformerForMigTest{}, PlaintextMigrationConfig{})
	require.NoError(t, err)

	result, err := m.MigrateConfigEntries(context.Background())
	require.NoError(t, err, "CAS miss must not be treated as an error")
	assert.Equal(t, 0, result.Processed, "Processed must be 0 when CAS predicate fails")
	assert.Equal(t, 1, result.Skipped, "Skipped must be 1 when concurrent writer won")
	// Exec was still called (we did try the UPDATE).
	assert.Len(t, db.execCalls, 1)
}

// ---------------------------------------------------------------------------
// TestPlaintextMigration_ConfigVersions_AADBinding (B2)
// ---------------------------------------------------------------------------

// TestPlaintextMigration_ConfigVersions_AADBinding verifies that the migrator
// uses AADForVersion (not AADForConfig) when migrating config_versions rows.
//
// Test flow:
//  1. Migrate a plaintext config_versions row → ciphertext stored in db.execCalls.
//  2. Decrypt with the CORRECT AAD (AADForVersion) → plaintext matches.
//  3. Decrypt with AADForConfig (wrong domain) → must fail — cross-domain replay blocked.
func TestPlaintextMigration_ConfigVersions_AADBinding(t *testing.T) {
	ctx := context.Background()

	configID := "550e8400-e29b-41d4-a716-446655440000"
	value := "super-secret-version-value"

	tr := &aadAwareTransformer{keyID: "test-key-v1"}
	db := &batchFakeDB{
		rows: []migrationRow{
			{id: "cv-row-1", key: configID, value: value, sensitive: true},
		},
	}
	m, err := newPlaintextMigrator(db, tr, PlaintextMigrationConfig{BatchSize: 10})
	require.NoError(t, err)

	result, err := m.MigrateConfigVersions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Processed)
	require.Len(t, db.execCalls, 1)

	// Extract the ciphertext written to the DB.
	ct, ok := db.execCalls[0].args[0].([]byte)
	require.True(t, ok, "first arg of UPDATE must be []byte ciphertext")

	// Correct AAD: AADForVersion (matches config_repo.go encryptVersionValue path).
	correctAAD := configcrypto.AADForVersion(cellID, configID)
	pt, err := tr.Decrypt(ctx, ct, "test-key-v1", nil, nil, correctAAD)
	require.NoError(t, err, "Decrypt with AADForVersion must succeed")
	assert.Equal(t, value, string(pt), "decrypted plaintext must match original")

	// Wrong AAD domain: AADForConfig with the same configID string must fail.
	wrongAAD := configcrypto.AADForConfig(cellID, configID)
	_, err = tr.Decrypt(ctx, ct, "test-key-v1", nil, nil, wrongAAD)
	require.Error(t, err, "Decrypt with AADForConfig (wrong domain) must fail")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be errcode.Error")
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}
