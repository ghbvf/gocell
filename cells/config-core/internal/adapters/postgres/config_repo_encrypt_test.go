package postgres

// config_repo_encrypt_test.go verifies the encryption behaviour added in
// PR-CC-VALUE-ENCRYPT (task 3.9). Tests are in the `postgres` package so
// they can use the unexported helpers (newConfigRepositoryFromDBTX, mockDB).
//
// Coverage goals:
//   - sensitive=true Create writes cipher columns and sets value to "".
//   - sensitive=true Update writes cipher columns.
//   - GetByKey decrypts sensitive row and returns plaintext.
//   - GetByKey returns ErrConfigDecryptFailed on decryption failure.
//   - GetByKey marks entry.Stale=true when keyID differs from current key.
//   - PublishVersion writes cipher columns for sensitive version.
//   - GetVersion decrypts sensitive version row.
//   - sensitive=false paths are unaffected (no encryption).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fake transformer for tests
// ---------------------------------------------------------------------------

// fakeValueTransformer is a deterministic in-memory transformer that
// "encrypts" by XOR-ing with 0x55 and records the AAD used during Encrypt.
// Round-trip: Encrypt + Decrypt with matching AAD returns the original plaintext.
// AAD mismatch: Decrypt with a different AAD than was used during Encrypt returns
// ErrKeyProviderDecryptFailed — exercising the repo-layer cross-row replay protection.
type fakeValueTransformer struct {
	currentKeyID   string
	failDecrypt    bool
	lastEncryptAAD []byte // records AAD from the most recent Encrypt call
}

// CurrentKeyID implements the hasCurrent interface used by ConfigRepository
// to compute the staleness signal.
func (f *fakeValueTransformer) CurrentKeyID(_ context.Context) (string, error) {
	return f.currentKeyID, nil
}

const fakeNonce = "FAKENC123456" // 12 bytes

func (f *fakeValueTransformer) Encrypt(_ context.Context, plaintext, aad []byte) ([]byte, string, []byte, []byte, error) {
	// Record the AAD so Decrypt can verify binding.
	aadCopy := make([]byte, len(aad))
	copy(aadCopy, aad)
	f.lastEncryptAAD = aadCopy

	// Fake cipher: XOR each byte with 0x55.
	ct := make([]byte, len(plaintext))
	for i, b := range plaintext {
		ct[i] = b ^ 0x55
	}
	return ct, f.currentKeyID, []byte(fakeNonce), []byte("edk-" + f.currentKeyID), nil
}

func (f *fakeValueTransformer) Decrypt(_ context.Context, ciphertext []byte, keyID string, _, _, aad []byte) ([]byte, error) {
	if f.failDecrypt {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "fake: forced decrypt failure")
	}
	// Check that the keyID is present (any non-empty keyID is OK for fake).
	if keyID == "" {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "fake: empty keyID")
	}
	// Enforce AAD binding: the AAD passed to Decrypt must match what was used
	// during Encrypt. This exercises the repo-layer cross-row replay protection.
	if string(aad) != string(f.lastEncryptAAD) {
		return nil, errcode.New(errcode.ErrConfigDecryptFailed,
			"fake: AAD mismatch — cross-row replay detected")
	}
	pt := make([]byte, len(ciphertext))
	for i, b := range ciphertext {
		pt[i] = b ^ 0x55
	}
	return pt, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newEncryptedRepoFromDBTX(db DBTX, tr crypto.ValueTransformer) *ConfigRepository {
	return &ConfigRepository{db: db, transformer: tr}
}

// ---------------------------------------------------------------------------
// Task 3.9 tests
// ---------------------------------------------------------------------------

// TestEncrypt_Create_SensitiveWritesCipherColumns verifies that Create for a
// sensitive=true entry calls transformer.Encrypt and writes cipher columns.
func TestEncrypt_Create_SensitiveWritesCipherColumns(t *testing.T) {
	db := &mockDB{}
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry := &domain.ConfigEntry{
		ID:        "cfg-1",
		Key:       "db_password",
		Value:     "s3cr3t",
		Sensitive: true,
	}

	err := repo.Create(context.Background(), entry)
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1, "Create must issue exactly one INSERT")
	sql := db.execCalls[0].sql
	assert.Contains(t, sql, "INSERT INTO config_entries")
	// Cipher columns must be present in INSERT.
	assert.Contains(t, sql, "value_cipher")
	assert.Contains(t, sql, "value_key_id")
	assert.Contains(t, sql, "value_edk")
	assert.Contains(t, sql, "value_nonce")

	// The plaintext value column should be written as empty string (not the secret).
	args := db.execCalls[0].args
	// Find the value arg position: args[2] = value in current INSERT order.
	// After encryption, the value arg should be "" (masked).
	// We check that the original plaintext is NOT present in any arg.
	for _, a := range args {
		if s, ok := a.(string); ok {
			assert.NotEqual(t, "s3cr3t", s, "plaintext must not appear in INSERT args")
		}
	}
}

// TestEncrypt_Create_NonSensitiveWritesPlaintext verifies that non-sensitive
// entries bypass encryption and write to the value column directly.
func TestEncrypt_Create_NonSensitiveWritesPlaintext(t *testing.T) {
	db := &mockDB{}
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry := &domain.ConfigEntry{
		ID:    "cfg-2",
		Key:   "app.name",
		Value: "GoCell",
	}

	err := repo.Create(context.Background(), entry)
	require.NoError(t, err)

	// Plain SQL should not contain cipher columns.
	sql := db.execCalls[0].sql
	assert.NotContains(t, sql, "value_cipher")

	// The plaintext must be in the args.
	found := false
	for _, a := range db.execCalls[0].args {
		if a == "GoCell" {
			found = true
		}
	}
	assert.True(t, found, "non-sensitive value must appear as plaintext in INSERT args")
}

// TestEncrypt_GetByKey_SensitiveDecryptsValue verifies transparent decryption.
func TestEncrypt_GetByKey_SensitiveDecryptsValue(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	// Build what the DB row looks like after encryption.
	original := "s3cr3t"
	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, []byte(original), crypto.AADForConfig("config-core", "db_password"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// Row columns: id, key, value, sensitive, version, created_at, updated_at,
			//             value_cipher, value_key_id, value_edk, value_nonce
			values: []any{"cfg-1", "db_password", "", true, 1, now, now,
				ct, keyID, edk, nonce},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.GetByKey(ctx, "db_password")
	require.NoError(t, err)
	assert.Equal(t, original, entry.Value, "GetByKey must return decrypted plaintext")
	assert.False(t, entry.Stale, "entry must not be stale (same key version)")
}

// TestEncrypt_GetByKey_SensitiveDecryptFailed_FailClosed verifies that
// decryption failure returns ErrConfigDecryptFailed (never plaintext/empty).
func TestEncrypt_GetByKey_SensitiveDecryptFailed_FailClosed(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1", failDecrypt: true}

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{"cfg-1", "db_password", "", true, 1, now, now,
				[]byte("someciphertext"), "local-aes-v1", []byte("edk"), []byte("nonce")},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	_, err := repo.GetByKey(ctx, "db_password")
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrConfigDecryptFailed, ec.Code)
}

// TestEncrypt_GetByKey_SensitiveStaleKey marks entry.Stale when key differs
// from the current key ID.
func TestEncrypt_GetByKey_SensitiveStaleKey(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v2"} // current is v2

	original := "stale-value"
	ct, _, nonce, edk, err := tr.Encrypt(ctx, []byte(original), crypto.AADForConfig("config-core", "old_key"))
	require.NoError(t, err)

	// Row was encrypted with v1 (old key).
	oldKeyID := "local-aes-v1"

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{"cfg-1", "old_key", "", true, 1, now, now,
				ct, oldKeyID, edk, nonce},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.GetByKey(ctx, "old_key")
	require.NoError(t, err)
	assert.True(t, entry.Stale, "entry must be stale when keyID != current key")
	assert.Equal(t, oldKeyID, entry.KeyID)
}

// TestEncrypt_GetByKey_NonSensitive_NoDecryption verifies that non-sensitive
// values are returned as-is without calling the transformer.
func TestEncrypt_GetByKey_NonSensitive_NoDecryption(t *testing.T) {
	ctx := context.Background()
	// Use a fail-decrypt transformer — non-sensitive path must never call it.
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1", failDecrypt: true}

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// value_cipher is nil → non-sensitive path
			values: []any{"cfg-1", "app.name", "GoCell", false, 1, now, now,
				nil, nil, nil, nil},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.GetByKey(ctx, "app.name")
	require.NoError(t, err)
	assert.Equal(t, "GoCell", entry.Value)
}

// TestEncrypt_Update_SensitiveWritesCipherColumns verifies that Update for a
// sensitive entry writes cipher columns.
func TestEncrypt_Update_SensitiveWritesCipherColumns(t *testing.T) {
	db := &mockDB{execAffected: 1}
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry := &domain.ConfigEntry{
		Key:       "api_key",
		Value:     "new-secret",
		Sensitive: true,
		Version:   2,
	}

	err := repo.Update(context.Background(), entry)
	require.NoError(t, err)

	sql := db.execCalls[0].sql
	assert.Contains(t, sql, "UPDATE config_entries")
	assert.Contains(t, sql, "value_cipher")
}

// TestEncrypt_PublishVersion_SensitiveWritesCipherColumns verifies that
// PublishVersion for a sensitive version writes cipher columns.
func TestEncrypt_PublishVersion_SensitiveWritesCipherColumns(t *testing.T) {
	db := &mockDB{}
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}
	repo := newEncryptedRepoFromDBTX(db, tr)

	now := time.Now()
	version := &domain.ConfigVersion{
		ID:          "cv-1",
		ConfigID:    "cfg-1",
		Version:     1,
		Value:       "secret-value",
		Sensitive:   true,
		PublishedAt: &now,
	}

	err := repo.PublishVersion(context.Background(), version)
	require.NoError(t, err)

	sql := db.execCalls[0].sql
	assert.Contains(t, sql, "INSERT INTO config_versions")
	assert.Contains(t, sql, "value_cipher")
}

// TestEncrypt_GetVersion_SensitiveDecryptsValue verifies transparent decryption
// of published versions. The AAD for config_versions uses v.ConfigID as the key
// (see encryptValue call in PublishVersion: encryptValue(ctx, version.ConfigID, ...)).
func TestEncrypt_GetVersion_SensitiveDecryptsValue(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "published-secret"
	// Use "cfg-1" as the AAD key — matches v.ConfigID used by GetVersion decryptValue.
	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, []byte(original), crypto.AADForConfig("config-core", "cfg-1"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// id, config_id, version, value, sensitive, published_at,
			// value_cipher, value_key_id, value_edk, value_nonce
			values: []any{"cv-1", "cfg-1", 1, "", true, &now,
				ct, keyID, edk, nonce},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	version, err := repo.GetVersion(ctx, "cfg-1", 1)
	require.NoError(t, err)
	assert.Equal(t, original, version.Value, "GetVersion must return decrypted plaintext")
}

// TestConfigRepo_GetByKey_Sensitive_LegacyPlaintext_ReturnsErr verifies that
// reading a sensitive entry with no value_cipher (legacy row, pre-migration)
// fails closed with ErrConfigDecryptFailed instead of silently returning plaintext.
func TestConfigRepo_GetByKey_Sensitive_LegacyPlaintext_ReturnsErr(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// Simulate legacy row: sensitive=true, value_cipher=nil, value_key_id=nil
			values: []any{"cfg-1", "db_password", "legacy-plaintext", true, 1, now, now,
				nil, nil, nil, nil},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	_, err := repo.GetByKey(ctx, "db_password")
	require.Error(t, err, "legacy sensitive plaintext must return error (fail-closed)")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrConfigDecryptFailed, ec.Code)
	assert.Contains(t, err.Error(), "legacy plaintext")
}

// TestConfigRepo_Decrypt_AADMismatch_FailsClosed verifies that the repo returns
// ErrConfigDecryptFailed when the transformer detects an AAD mismatch.
// This exercises the cross-row replay protection at the repo boundary.
func TestConfigRepo_Decrypt_AADMismatch_FailsClosed(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	// Encrypt value for key "db_password".
	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, []byte("secret"), crypto.AADForConfig("config-core", "db_password"))
	require.NoError(t, err)

	// Now tamper: change the fake's lastEncryptAAD to simulate it having been
	// originally encrypted for a different key (cross-row replay scenario).
	// We overwrite it with AAD for a different key so Decrypt will detect mismatch.
	_, _, _, _, _ = tr.Encrypt(ctx, []byte("other"), crypto.AADForConfig("config-core", "other_key"))
	// lastEncryptAAD is now "other_key" AAD, but the DB row has "db_password" AAD.

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{"cfg-1", "db_password", "", true, 1, now, now,
				ct, keyID, edk, nonce},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	// GetByKey must fail-closed when AAD doesn't match.
	_, err = repo.GetByKey(ctx, "db_password")
	require.Error(t, err, "AAD mismatch must return error")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrConfigDecryptFailed, ec.Code)
}
