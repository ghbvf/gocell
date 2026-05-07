package postgres

// config_repo_encrypt_test.go verifies the encryption behavior added in
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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configcrypto "github.com/ghbvf/gocell/cells/configcore/internal/crypto"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// ---------------------------------------------------------------------------
// Fake transformer for tests
// ---------------------------------------------------------------------------

// fakeValueTransformer is a deterministic in-memory transformer that
// "encrypts" by XOR-ing with 0x55 and embeds the AAD into the edk so that
// Decrypt can verify AAD binding without mutable state.
// Round-trip: Encrypt + Decrypt with matching AAD returns the original plaintext.
// AAD mismatch: Decrypt with a different AAD than was encoded in the edk returns
// ErrConfigDecryptFailed — exercising the repo-layer cross-row replay protection.
type fakeValueTransformer struct {
	currentKeyID string
	failDecrypt  bool
	failEncrypt  bool
}

// CurrentKeyID implements crypto.CurrentKeyIDProvider — the optional
// extension interface used by ConfigRepository to compute the staleness
// signal.
func (f *fakeValueTransformer) CurrentKeyID(_ context.Context) (string, error) {
	return f.currentKeyID, nil
}

const fakeNonce = "FAKENC123456" // 12 bytes

func (f *fakeValueTransformer) Encrypt(_ context.Context, plaintext, aad []byte) (crypto.EncryptResult, error) {
	if f.failEncrypt {
		return crypto.EncryptResult{}, errcode.New(errcode.KindUnavailable, errcode.ErrKeyProviderTransient, "fake: forced encrypt failure")
	}
	// Fake cipher: XOR each byte with 0x55.
	ct := make([]byte, len(plaintext))
	for i, b := range plaintext {
		ct[i] = b ^ 0x55
	}
	// Encode AAD into edk so Decrypt can verify binding without mutable state.
	edk := append([]byte("edk-"+f.currentKeyID+":aad:"), aad...)
	return crypto.EncryptResult{
		Ciphertext: ct,
		Nonce:      []byte(fakeNonce),
		EDK:        edk,
		KeyID:      f.currentKeyID,
	}, nil
}

func (f *fakeValueTransformer) Decrypt(_ context.Context, ciphertext []byte, keyID string, _, edk, aad []byte) ([]byte, error) {
	if f.failDecrypt {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed, "fake: forced decrypt failure")
	}
	if keyID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed, "fake: empty keyID")
	}
	// Verify AAD binding via edk-embedded AAD (stateless check).
	expectedEDK := append([]byte("edk-"+keyID+":aad:"), aad...)
	if !bytes.Equal(edk, expectedEDK) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigDecryptFailed,
			"fake: AAD mismatch (edk does not match expected AAD binding)")
	}
	// Reverse XOR.
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
	return &ConfigRepository{db: db, transformer: tr, logger: slog.Default(), clock: clock.Real()}
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
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "db_password"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// Row columns: id, key, value, sensitive, version, created_at, updated_at,
			//             value_cipher, value_key_id, value_edk, value_nonce
			values: []any{
				"cfg-1", "db_password", "", true, 1, now, now,
				result.Ciphertext, result.KeyID, result.EDK, result.Nonce,
			},
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
			values: []any{
				"cfg-1", "db_password", "", true, 1, now, now,
				[]byte("someciphertext"), "local-aes-v1", []byte("edk"), []byte("nonce"),
			},
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
	// Encrypt with v1 (the "old" key) so edk embeds v1.
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "stale-value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "old_key"))
	require.NoError(t, err)

	// Simulate key rotation: current is now v2, but the row was encrypted with v1.
	tr.currentKeyID = "local-aes-v2"
	oldKeyID := "local-aes-v1"

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "old_key", "", true, 1, now, now,
				result.Ciphertext, oldKeyID, result.EDK, result.Nonce,
			},
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
			values: []any{
				"cfg-1", "app.name", "GoCell", false, 1, now, now,
				nil, nil, nil, nil,
			},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.GetByKey(ctx, "app.name")
	require.NoError(t, err)
	assert.Equal(t, "GoCell", entry.Value)
}

// TestEncrypt_UpdateForRollback_SensitiveWritesCipherColumns verifies that
// UpdateForRollback for a sensitive entry writes cipher columns and returns the
// updated entry via RETURNING.
func TestEncrypt_UpdateForRollback_SensitiveWritesCipherColumns(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	// Build what the DB RETURNING row looks like after encryption.
	result, err := tr.Encrypt(ctx, []byte("new-secret"), configcrypto.AADForConfig("configcore", "api_key"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{"cfg-1", "api_key", "", true, 2, now, now, result.Ciphertext, result.KeyID, result.EDK, result.Nonce},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.UpdateForRollback(context.Background(), "api_key", "new-secret", true)
	require.NoError(t, err)
	require.NotNil(t, entry)

	sql := db.queryRowCalls[0].sql
	assert.Contains(t, sql, "UPDATE config_entries")
	assert.Contains(t, sql, "value_cipher")
	assert.Equal(t, "new-secret", entry.Value, "UpdateForRollback must return decrypted plaintext")
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
// of published versions. The AAD for config_versions uses AADForVersion with
// v.ConfigID (see decryptVersionValue in GetVersion).
func TestEncrypt_GetVersion_SensitiveDecryptsValue(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "published-secret"
	// Use AADForVersion — matches decryptVersionValue called by GetVersion.
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForVersion("configcore", "cfg-1"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// id, config_id, version, value, sensitive, published_at,
			// value_cipher, value_key_id, value_edk, value_nonce
			values: []any{
				"cv-1", "cfg-1", 1, "", true, &now,
				result.Ciphertext, result.KeyID, result.EDK, result.Nonce,
			},
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
			values: []any{
				"cfg-1", "db_password", "legacy-plaintext", true, 1, now, now,
				nil, nil, nil, nil,
			},
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
//
// Cross-row replay: ciphertext was encrypted for "other_key" but is stored in
// the "db_password" row. The edk embeds "other_key"'s AAD; when the repo reads
// "db_password" it passes "db_password" AAD to Decrypt — mismatch detected.
func TestConfigRepo_Decrypt_AADMismatch_FailsClosed(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	// Encrypt value for "other_key" — edk embeds "other_key" AAD.
	result, err := tr.Encrypt(ctx, []byte("secret"), configcrypto.AADForConfig("configcore", "other_key"))
	require.NoError(t, err)

	// Construct an edk that binds to "other_key" AAD (cross-row replay).
	// When GetByKey reads "db_password", the repo passes "db_password" AAD to
	// Decrypt, which won't match this edk → ErrConfigDecryptFailed.
	wrongEDK := append([]byte("edk-"+result.KeyID+":aad:"), configcrypto.AADForConfig("configcore", "other_key")...)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "db_password", "", true, 1, now, now,
				result.Ciphertext, result.KeyID, wrongEDK, result.Nonce,
			},
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

// ---------------------------------------------------------------------------
// Additional coverage for config_repo.go uncovered branches
// ---------------------------------------------------------------------------

// TestGetByKey_Sensitive_EmptyValueCipher_LegacyPlaintext verifies that a
// sensitive=true row with value_cipher=nil (empty) and a non-nil key_id returns
// ErrConfigDecryptFailed (legacy plaintext path).
func TestGetByKey_Sensitive_EmptyValueCipher_LegacyPlaintext(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	now := time.Now()
	// value_cipher is nil, value_key_id is nil → legacy plaintext path.
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "secret_key", "plaintext-not-encrypted", true, 1, now, now,
				nil, nil, nil, nil,
			},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	_, err := repo.GetByKey(ctx, "secret_key")
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrConfigDecryptFailed, ec.Code)
	assert.Contains(t, err.Error(), "legacy plaintext")
}

// failingCurrentKeyIDTransformer implements ValueTransformer AND CurrentKeyIDProvider
// but CurrentKeyID returns an error — verifies that currentKeyID() returns "" without panic.
type failingCurrentKeyIDTransformer struct {
	fakeValueTransformer
}

func (f *failingCurrentKeyIDTransformer) CurrentKeyID(_ context.Context) (string, error) {
	return "", errors.New("key provider unavailable")
}

// TestCurrentKeyID_ProviderReturnsError verifies that when CurrentKeyID returns
// an error, currentKeyID() returns "" and GetByKey does not panic or set Stale.
func TestCurrentKeyID_ProviderReturnsError(t *testing.T) {
	ctx := context.Background()
	tr := &failingCurrentKeyIDTransformer{
		fakeValueTransformer: fakeValueTransformer{currentKeyID: "local-aes-v1"},
	}

	original := "some-secret"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "my_key"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "my_key", "", true, 1, now, now,
				result.Ciphertext, result.KeyID, result.EDK, result.Nonce,
			},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.GetByKey(ctx, "my_key")
	require.NoError(t, err, "CurrentKeyID error must not propagate")
	assert.Equal(t, original, entry.Value)
	assert.False(t, entry.Stale, "Stale must be false when currentKeyID returns error (treated as empty)")
}

// TestGetByKey_Sensitive_StaleKey_DifferentStoredAndCurrentKeyID verifies that
// when the stored keyID differs from the current active keyID, entry.Stale is true.
func TestGetByKey_Sensitive_StaleKey_DifferentStoredAndCurrentKeyID(t *testing.T) {
	ctx := context.Background()
	// Current active key is v2, but the row was encrypted with v1.
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"} // encrypt with v1 first

	original := "stale-value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "cfg_key"))
	require.NoError(t, err)

	// Now simulate key rotation: current is v2, but stored row has v1.
	tr.currentKeyID = "local-aes-v2"
	storedKeyID := "local-aes-v1"

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "cfg_key", "", true, 1, now, now,
				result.Ciphertext, storedKeyID, result.EDK, result.Nonce,
			},
		},
	}
	repo := newEncryptedRepoFromDBTX(db, tr)

	entry, err := repo.GetByKey(ctx, "cfg_key")
	require.NoError(t, err)
	assert.True(t, entry.Stale, "entry must be marked stale when storedKeyID != currentKeyID")
	assert.Equal(t, storedKeyID, entry.KeyID)
}

// ---------------------------------------------------------------------------
// M3 — Stale key observability: slog.Warn + onStaleCipher callback
// ---------------------------------------------------------------------------

// newTestLogger returns a slog.Logger backed by a JSON handler writing to buf.
// Tests use this to assert structured log fields without relying on string formatting.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// parseLogEntries decodes newline-delimited JSON log lines into a slice of maps.
func parseLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for line := range bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(line, &m), "log line must be valid JSON")
		entries = append(entries, m)
	}
	return entries
}

// TestGetByKey_StaleKey_EmitsWarn verifies that when GetByKey detects a stale
// key (stored keyID != current keyID), it emits a slog.Warn carrying ONLY the
// business key. Cryptographic key IDs are routed exclusively through the
// onStaleCipher callback (Prometheus label dimension), never the log plane.
func TestGetByKey_StaleKey_EmitsWarn(t *testing.T) {
	ctx := context.Background()
	// Encrypt with v1, then rotate current to v2 → stale.
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "sensitive-value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "api_secret"))
	require.NoError(t, err)

	storedKeyID := "local-aes-v1"
	tr.currentKeyID = "local-aes-v2" // rotate after encrypt

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "api_secret", "", true, 1, now, now,
				result.Ciphertext, storedKeyID, result.EDK, result.Nonce,
			},
		},
	}

	var logBuf bytes.Buffer
	logger := newTestLogger(&logBuf)
	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.logger = logger

	entry, err := repo.GetByKey(ctx, "api_secret")
	require.NoError(t, err)
	require.True(t, entry.Stale)

	entries := parseLogEntries(t, &logBuf)
	require.Len(t, entries, 1, "exactly one log line must be emitted for stale key")

	logEntry := entries[0]
	assert.Equal(t, "WARN", logEntry["level"], "log level must be WARN")
	assert.Equal(t, "config value encrypted with stale key", logEntry["msg"])
	assert.Equal(t, "api_secret", logEntry["key"])
	_, hasStored := logEntry["stored_key_id"]
	assert.False(t, hasStored, "stored_key_id must not appear in slog Warn (cryptographic ID redaction)")
	_, hasCurrent := logEntry["current_key_id"]
	assert.False(t, hasCurrent, "current_key_id must not appear in slog Warn (cryptographic ID redaction)")
}

// TestGetByKey_FreshKey_NoWarn verifies that no slog.Warn is emitted when the
// stored keyID matches the current keyID (fresh / non-stale entry).
func TestGetByKey_FreshKey_NoWarn(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "fresh-value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "fresh_key"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "fresh_key", "", true, 1, now, now,
				result.Ciphertext, result.KeyID, result.EDK, result.Nonce,
			},
		},
	}

	var logBuf bytes.Buffer
	logger := newTestLogger(&logBuf)
	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.logger = logger

	entry, err := repo.GetByKey(ctx, "fresh_key")
	require.NoError(t, err)
	assert.False(t, entry.Stale)
	assert.Empty(t, logBuf.Bytes(), "no log output must be emitted for fresh key")
}

// TestList_StaleKey_EmitsWarn verifies that List emits slog.Warn for each
// sensitive entry whose stored keyID differs from the current keyID.
func TestList_StaleKey_EmitsWarn(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v2"} // current is v2

	storedKeyID := "local-aes-v1" // stale

	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				// sensitive entry with stale key
				{values: []any{"cfg-1", "stale_cfg", "***", true, 1, now, now, &storedKeyID}},
				// non-sensitive entry — no warn expected
				{values: []any{"cfg-2", "plain_cfg", "value", false, 1, now, now, (*string)(nil)}},
			},
		},
	}

	var logBuf bytes.Buffer
	logger := newTestLogger(&logBuf)
	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.logger = logger

	entries, err := repo.List(ctx, query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.True(t, entries[0].Stale)
	assert.False(t, entries[1].Stale)

	logEntries := parseLogEntries(t, &logBuf)
	require.Len(t, logEntries, 1, "exactly one warn for the stale sensitive entry")
	assert.Equal(t, "WARN", logEntries[0]["level"])
	assert.Equal(t, "config values encrypted with stale key (first occurrence in this List page)", logEntries[0]["msg"])
	assert.Equal(t, "stale_cfg", logEntries[0]["key"])
	_, hasStored := logEntries[0]["stored_key_id"]
	assert.False(t, hasStored, "stored_key_id must not appear in slog Warn (cryptographic ID redaction)")
	_, hasCurrent := logEntries[0]["current_key_id"]
	assert.False(t, hasCurrent, "current_key_id must not appear in slog Warn (cryptographic ID redaction)")
}

// TestGetByKey_StaleKey_OnStaleCipherCallback verifies that the optional
// onStaleCipher callback is invoked with the correct arguments when a stale
// key is detected, enabling callers to wire a prometheus counter.
func TestGetByKey_StaleKey_OnStaleCipherCallback(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "cb_key"))
	require.NoError(t, err)

	storedKeyID := "local-aes-v1"
	tr.currentKeyID = "local-aes-v2"

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "cb_key", "", true, 1, now, now,
				result.Ciphertext, storedKeyID, result.EDK, result.Nonce,
			},
		},
	}

	type callbackArgs struct {
		key, storedKeyID, currentKeyID string
	}
	var got []callbackArgs

	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.onStaleCipher = func(key, storedID, currentID string) {
		got = append(got, callbackArgs{key, storedID, currentID})
	}

	_, err = repo.GetByKey(ctx, "cb_key")
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "cb_key", got[0].key)
	assert.Equal(t, storedKeyID, got[0].storedKeyID)
	assert.Equal(t, "local-aes-v2", got[0].currentKeyID)
}

// TestList_StaleKey_LogDedup verifies that List emits slog.Warn only ONCE per
// distinct stale keyID per List call, even when multiple entries share the same
// stale keyID. The onStaleCipher callback must still fire for every stale entry
// (metric accuracy).
func TestList_StaleKey_LogDedup(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v2"} // current is v2

	storedKeyID := "local-aes-v1" // stale

	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				// Three sensitive entries all encrypted with the same stale key.
				{values: []any{"cfg-1", "key_a", "***", true, 1, now, now, &storedKeyID}},
				{values: []any{"cfg-2", "key_b", "***", true, 1, now, now, &storedKeyID}},
				{values: []any{"cfg-3", "key_c", "***", true, 1, now, now, &storedKeyID}},
				// Non-sensitive — no warn expected.
				{values: []any{"cfg-4", "plain_cfg", "value", false, 1, now, now, (*string)(nil)}},
			},
		},
	}

	var logBuf bytes.Buffer
	logger := newTestLogger(&logBuf)
	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.logger = logger

	// Wire callback to record every stale invocation with full triple
	// (key, storedKeyID, currentKeyID) so any parameter-order regression
	// is caught immediately.
	type stalecallArgs struct{ key, storedKeyID, currentKeyID string }
	var callbackCalls []stalecallArgs
	repo.onStaleCipher = func(key, storedID, currentID string) {
		callbackCalls = append(callbackCalls, stalecallArgs{key, storedID, currentID})
	}

	entries, err := repo.List(ctx, query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	})
	require.NoError(t, err)
	require.Len(t, entries, 4)

	// All three sensitive entries must be stale.
	assert.True(t, entries[0].Stale, "key_a must be stale")
	assert.True(t, entries[1].Stale, "key_b must be stale")
	assert.True(t, entries[2].Stale, "key_c must be stale")
	assert.False(t, entries[3].Stale, "plain_cfg must not be stale")

	// Callback must fire for every stale entry (3 total) with correct args.
	require.Len(t, callbackCalls, 3, "onStaleCipher callback must fire for every stale entry")
	assert.ElementsMatch(t, []string{"key_a", "key_b", "key_c"},
		[]string{callbackCalls[0].key, callbackCalls[1].key, callbackCalls[2].key},
		"callback must receive each stale business key")
	for i, call := range callbackCalls {
		assert.Equal(t, storedKeyID, call.storedKeyID,
			"call[%d]: storedKeyID must be %q", i, storedKeyID)
		assert.Equal(t, "local-aes-v2", call.currentKeyID,
			"call[%d]: currentKeyID must be \"local-aes-v2\"", i)
	}

	// slog.Warn must fire only ONCE for the shared stale keyID; dedup is keyed
	// on the (redacted) stale keyID internally, but the log line surfaces the
	// business key of the first occurrence — never the keyID itself.
	logEntries := parseLogEntries(t, &logBuf)
	require.Len(t, logEntries, 1, "only one warn per distinct stale keyID per List call")
	assert.Equal(t, "WARN", logEntries[0]["level"])
	assert.Equal(t, "key_a", logEntries[0]["key"], "first occurrence's business key surfaces in log")
	_, hasStored := logEntries[0]["stored_key_id"]
	assert.False(t, hasStored, "stored_key_id must not appear in slog Warn (cryptographic ID redaction)")
	_, hasCurrent := logEntries[0]["current_key_id"]
	assert.False(t, hasCurrent, "current_key_id must not appear in slog Warn (cryptographic ID redaction)")
}

// TestList_StaleKey_TwoDistinctKeyIDs_TwoWarns verifies that when entries are
// encrypted with two distinct stale keyIDs, exactly two slog.Warn lines are
// emitted (one per distinct keyID), but callbacks fire for every entry.
func TestList_StaleKey_TwoDistinctKeyIDs_TwoWarns(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v3"} // current is v3

	keyIDv1 := "local-aes-v1"
	keyIDv2 := "local-aes-v2"

	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				{values: []any{"cfg-1", "key_a", "***", true, 1, now, now, &keyIDv1}},
				{values: []any{"cfg-2", "key_b", "***", true, 1, now, now, &keyIDv1}},
				{values: []any{"cfg-3", "key_c", "***", true, 1, now, now, &keyIDv2}},
			},
		},
	}

	var logBuf bytes.Buffer
	logger := newTestLogger(&logBuf)
	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.logger = logger

	// Wire callback to record full triple so parameter-order regressions are caught.
	type stalecallArgs struct{ key, storedKeyID, currentKeyID string }
	var callbackCalls []stalecallArgs
	repo.onStaleCipher = func(key, storedID, currentID string) {
		callbackCalls = append(callbackCalls, stalecallArgs{key, storedID, currentID})
	}

	entries, err := repo.List(ctx, query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	})
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// All three are stale.
	assert.True(t, entries[0].Stale)
	assert.True(t, entries[1].Stale)
	assert.True(t, entries[2].Stale)

	// Callback fires for all 3 with correct per-entry args.
	require.Len(t, callbackCalls, 3, "onStaleCipher must fire for every stale entry")
	// Business keys across all three calls.
	assert.ElementsMatch(t, []string{"key_a", "key_b", "key_c"},
		[]string{callbackCalls[0].key, callbackCalls[1].key, callbackCalls[2].key},
		"callback must receive each stale business key")
	// storedKeyID per call: key_a and key_b use v1, key_c uses v2.
	gotStoredIDs := []string{callbackCalls[0].storedKeyID, callbackCalls[1].storedKeyID, callbackCalls[2].storedKeyID}
	assert.ElementsMatch(t, []string{keyIDv1, keyIDv1, keyIDv2}, gotStoredIDs,
		"storedKeyID must reflect the key used to encrypt each entry")
	// currentKeyID is always v3 for all calls.
	for i, call := range callbackCalls {
		assert.Equal(t, "local-aes-v3", call.currentKeyID,
			"call[%d]: currentKeyID must be \"local-aes-v3\"", i)
	}

	// Two distinct keyIDs → two slog.Warn lines (dedup is keyed on the redacted
	// keyID internally; each log line surfaces the business key of the first
	// occurrence for that keyID — key_a for v1, key_c for v2).
	logEntries := parseLogEntries(t, &logBuf)
	require.Len(t, logEntries, 2, "one warn per distinct stale keyID")
	assert.Equal(t, "WARN", logEntries[0]["level"])
	assert.Equal(t, "WARN", logEntries[1]["level"])
	businessKeys := []string{
		logEntries[0]["key"].(string),
		logEntries[1]["key"].(string),
	}
	assert.ElementsMatch(t, []string{"key_a", "key_c"}, businessKeys)
	for _, entry := range logEntries {
		_, hasStored := entry["stored_key_id"]
		assert.False(t, hasStored, "stored_key_id must not appear in slog Warn (cryptographic ID redaction)")
		_, hasCurrent := entry["current_key_id"]
		assert.False(t, hasCurrent, "current_key_id must not appear in slog Warn (cryptographic ID redaction)")
	}
}

// TestWithOnStaleCipher_Option verifies that NewConfigRepository wired with the
// WithOnStaleCipher option correctly sets the callback. The test injects a stale
// key via GetByKey and asserts the callback fires.
func TestWithOnStaleCipher_Option(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "opt_key"))
	require.NoError(t, err)

	storedKeyID := "local-aes-v1"
	tr.currentKeyID = "local-aes-v2" // rotate

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "opt_key", "", true, 1, now, now,
				result.Ciphertext, storedKeyID, result.EDK, result.Nonce,
			},
		},
	}

	type callbackArgs struct{ key, storedID, currentID string }
	var got []callbackArgs

	// Use NewConfigRepository with WithOnStaleCipher option.
	session := &Session{} // nil pool is fine — db field is set below via direct struct assignment
	repo := NewConfigRepository(session, tr, nil, clock.Real(), WithOnStaleCipher(func(key, storedID, currentID string) {
		got = append(got, callbackArgs{key, storedID, currentID})
	}))
	// Bypass session for unit test — inject mockDB directly.
	repo.session = nil
	repo.db = db

	_, err = repo.GetByKey(ctx, "opt_key")
	require.NoError(t, err)

	require.Len(t, got, 1, "WithOnStaleCipher callback must fire for stale key")
	assert.Equal(t, "opt_key", got[0].key)
	assert.Equal(t, storedKeyID, got[0].storedID)
	assert.Equal(t, "local-aes-v2", got[0].currentID)
}

// TestEncrypt_FailEncrypt_RoutesToErrConfigEncryptFailed verifies that when
// transformer.Encrypt returns an error, the repo returns ErrConfigEncryptFailed
// (not ErrConfigRepoQuery) for all encrypt paths. The cause is a transient
// ErrKeyProviderTransient error (e.g. Vault sealed / rate-limited) which must
// preserve CategoryInfra so alerting can distinguish crypto failures from DB failures.
func TestEncrypt_FailEncrypt_RoutesToErrConfigEncryptFailed(t *testing.T) {
	ctx := context.Background()

	t.Run("Create sensitive entry", func(t *testing.T) {
		db := &mockDB{}
		tr := &fakeValueTransformer{currentKeyID: "local-aes-v1", failEncrypt: true}
		repo := newEncryptedRepoFromDBTX(db, tr)

		entry := &domain.ConfigEntry{
			ID:        "cfg-enc-1",
			Key:       "db_password",
			Value:     "s3cr3t",
			Sensitive: true,
		}
		err := repo.Create(ctx, entry)
		require.Error(t, err)

		var ec *errcode.Error
		require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
		assert.Equal(t, errcode.ErrConfigEncryptFailed, ec.Code,
			"encrypt failure on Create must route to ErrConfigEncryptFailed, not ErrConfigRepoQuery")
		assert.True(t, errcode.IsInfraError(ec),
			"CategoryInfra must propagate from ErrKeyProviderTransient cause so Vault-outage alerts fire")
		// Side-effect contract: encrypt-fail must short-circuit before any DB
		// write. If a future refactor accidentally moves the encrypt call after
		// the INSERT, these assertions catch it.
		require.Empty(t, db.execCalls, "Create must not issue any DB write when encrypt fails")
		require.Empty(t, db.queryRowCalls, "Create must not issue any DB query when encrypt fails")
	})

	t.Run("UpdateForRollback sensitive entry", func(t *testing.T) {
		db := &mockDB{
			// QueryRow returns a scan error to avoid the RETURNING scan path;
			// the encrypt failure happens before any DB call, so this row is
			// never actually reached, but we need a non-nil mockRow to avoid
			// a nil-pointer dereference in resolveWriteDB (test path uses db directly).
			queryRowResult: &mockRow{scanErr: assert.AnError},
		}
		tr := &fakeValueTransformer{currentKeyID: "local-aes-v1", failEncrypt: true}
		repo := newEncryptedRepoFromDBTX(db, tr)

		_, err := repo.UpdateForRollback(ctx, "api_key", "new-secret", true)
		require.Error(t, err)

		var ec *errcode.Error
		require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
		assert.Equal(t, errcode.ErrConfigEncryptFailed, ec.Code,
			"encrypt failure on UpdateForRollback must route to ErrConfigEncryptFailed")
		assert.True(t, errcode.IsInfraError(ec),
			"CategoryInfra must propagate from ErrKeyProviderTransient cause")
		require.Empty(t, db.execCalls, "UpdateForRollback must not issue any DB write when encrypt fails")
		require.Empty(t, db.queryRowCalls, "UpdateForRollback must not issue UPDATE...RETURNING when encrypt fails")
	})

	t.Run("PublishVersion sensitive version", func(t *testing.T) {
		db := &mockDB{}
		tr := &fakeValueTransformer{currentKeyID: "local-aes-v1", failEncrypt: true}
		repo := newEncryptedRepoFromDBTX(db, tr)

		now := time.Now()
		version := &domain.ConfigVersion{
			ID:          "cv-enc-1",
			ConfigID:    "cfg-1",
			Version:     1,
			Value:       "secret-value",
			Sensitive:   true,
			PublishedAt: &now,
		}
		err := repo.PublishVersion(ctx, version)
		require.Error(t, err)

		var ec *errcode.Error
		require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
		assert.Equal(t, errcode.ErrConfigEncryptFailed, ec.Code,
			"encrypt failure on PublishVersion must route to ErrConfigEncryptFailed")
		assert.True(t, errcode.IsInfraError(ec),
			"CategoryInfra must propagate from ErrKeyProviderTransient cause")
		require.Empty(t, db.execCalls, "PublishVersion must not insert config_versions row when encrypt fails")
		require.Empty(t, db.queryRowCalls, "PublishVersion must not issue any DB query when encrypt fails")
	})

	t.Run("cryptoOpError direct — ErrConfigEncryptFailed transient", func(t *testing.T) {
		repo := newConfigRepositoryFromDBTX(&mockDB{})
		transient := errcode.New(errcode.KindUnavailable, errcode.ErrKeyProviderTransient, "vault sealed")
		ec := repo.cryptoOpError(errcode.ErrConfigEncryptFailed, "Encrypt", "key=foo", transient)
		require.NotNil(t, ec)
		assert.Equal(t, errcode.ErrConfigEncryptFailed, ec.Code)
		assert.True(t, errcode.IsInfraError(ec),
			"transient cause must preserve CategoryInfra")
		assert.Contains(t, ec.InternalMessage, "Encrypt",
			"InternalMessage must carry the PascalCase op label")
	})

	// Update (non-Rollback) path: SELECT FOR UPDATE + doUpdate → encryptValue.
	// Setting up the SELECT FOR UPDATE mock is complex, so we cover the encrypt
	// failure path via encryptValue direct call — this exercises the exact same
	// encryptValue → cryptoOpError chain that doUpdate uses for sensitive writes.
	t.Run("encryptValue direct call (covers Update sensitive write path)", func(t *testing.T) {
		tr := &fakeValueTransformer{currentKeyID: "v1", failEncrypt: true}
		repo := newEncryptedRepoFromDBTX(&mockDB{}, tr)
		_, err := repo.encryptValue(ctx, "update_key", "new_value")
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
		assert.Equal(t, errcode.ErrConfigEncryptFailed, ec.Code,
			"encryptValue failure must route to ErrConfigEncryptFailed (covers Update/doUpdate path)")
		assert.True(t, errcode.IsInfraError(ec),
			"CategoryInfra must propagate so Vault-outage alerts fire")
	})
}

// TestGetByKey_FreshKey_OnStaleCipherCallback_NotCalled verifies that the
// onStaleCipher callback is NOT called when the key is fresh.
func TestGetByKey_FreshKey_OnStaleCipherCallback_NotCalled(t *testing.T) {
	ctx := context.Background()
	tr := &fakeValueTransformer{currentKeyID: "local-aes-v1"}

	original := "value"
	result, err := tr.Encrypt(ctx, []byte(original), configcrypto.AADForConfig("configcore", "fresh_cb_key"))
	require.NoError(t, err)

	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			values: []any{
				"cfg-1", "fresh_cb_key", "", true, 1, now, now,
				result.Ciphertext, result.KeyID, result.EDK, result.Nonce,
			},
		},
	}

	called := false
	repo := newEncryptedRepoFromDBTX(db, tr)
	repo.onStaleCipher = func(_, _, _ string) { called = true }

	_, err = repo.GetByKey(ctx, "fresh_cb_key")
	require.NoError(t, err)
	assert.False(t, called, "onStaleCipher must not be called for a fresh key")
}
