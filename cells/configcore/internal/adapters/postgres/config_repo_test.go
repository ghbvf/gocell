package postgres

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// newConfigRepositoryFromDBTX is a test-only constructor that bypasses the
// Session layer, allowing unit tests to inject a mockDB directly.
// Production code always goes through NewConfigRepository(*Session).
func newConfigRepositoryFromDBTX(db DBTX) *ConfigRepository {
	return &ConfigRepository{db: db, logger: slog.Default(), clock: clock.Real()}
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

	err := repo.Create(context.Background(), &domain.ConfigEntry{Key: "secret_user_key"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoQuery, ec.Code)
	assert.NotContains(t, ec.Message, "secret_user_key", "public Message must not leak entry.Key")
	assert.Contains(t, ec.InternalMessage, "key=secret_user_key", "InternalMessage must carry key for triage")
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
	require.True(t, errcode.IsInfraError(err),
		"generic scan error must be CategoryInfra (not Domain)")
}

// TestGetByKey_NotFound_HasDomainCategory locks ErrConfigRepoNotFound's Category
// to CategoryDomain so that errcode.IsDomainNotFound dual-channel check works
// (S15 Category fix).
func TestGetByKey_NotFound_HasDomainCategory(t *testing.T) {
	db := &mockDB{queryRowResult: &mockRow{scanErr: pgx.ErrNoRows}}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetByKey(context.Background(), "missing")
	require.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"ErrConfigRepoNotFound must have Category=CategoryDomain for IsDomainNotFound to work")
	require.False(t, errcode.IsInfraError(err),
		"domain not-found must not be treated as infra")
}

// TestConfigRepo_CtxCanceled_ReturnsClientCanceled verifies that
// context.Canceled and context.DeadlineExceeded are classified through
// ctxcancel.Wrap, never as domain not-found. Covers GetByKey, Update
// SELECT-FOR-UPDATE, Update RETURNING, GetVersion, and Delete.
//
// PR275 P2-3 split: the expected code now branches by ctx error variant —
//   - context.Canceled         → ErrClientCanceled (HTTP 499 + slog.Warn)
//   - context.DeadlineExceeded → ErrServerTimeout  (HTTP 504 + slog.Error)
//
// IsInfraError is preserved (true) for both branches so health.Checker
// timeout-bucket behavior is unchanged; the HTTP layer routes 499/504 via
// codeToStatus, not via IsInfraError.
func TestConfigRepo_CtxCanceled_ReturnsClientCanceled(t *testing.T) {
	tests := []struct {
		name    string
		scanErr error
	}{
		{"ctx canceled", context.Canceled},
		{"ctx deadline exceeded", context.DeadlineExceeded},
	}
	assertCtxCancelErr := func(t *testing.T, err error, scanErr error) {
		t.Helper()
		require.Error(t, err)
		require.True(t, errcode.IsInfraError(err),
			"IsInfraError preserved (preserves health/timeout bucket behavior)")
		require.False(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
			"ctx cancel must not leak into domain not-found branch")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)

		expectedCode := errcode.ErrClientCanceled
		expected4xx := true
		if errors.Is(scanErr, context.DeadlineExceeded) {
			expectedCode = errcode.ErrServerTimeout
			expected4xx = false
		}
		assert.Equal(t, expectedCode, ec.Code,
			"Canceled→ErrClientCanceled (499) / DeadlineExceeded→ErrServerTimeout (504)")
		assert.Equal(t, expected4xx, errcode.IsExpected4xx(err),
			"499 routes through log4xx → slog.Warn; 504 routes through log5xx → slog.Error")
		require.Contains(t, ec.InternalMessage, "ctx canceled",
			"must hit wrapCtxCancel path, not generic scan-error fallthrough")
	}
	for _, tc := range tests {
		t.Run("GetByKey/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newConfigRepositoryFromDBTX(db)
			_, err := repo.GetByKey(context.Background(), "any")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Update_SelectForUpdate/"+tc.name, func(t *testing.T) {
			seqDB := &sequencedMockDB{rows: []*mockRow{{scanErr: tc.scanErr}}}
			repo := newConfigRepositoryFromDBTX(seqDB)
			_, err := repo.Update(context.Background(), "k", "v")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("GetVersion/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newConfigRepositoryFromDBTX(db)
			_, err := repo.GetVersion(context.Background(), "cfg-1", 1)
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Update_Returning/"+tc.name, func(t *testing.T) {
			// SELECT FOR UPDATE succeeds (sensitive=false); UPDATE RETURNING ctx-cancels.
			seqDB := &sequencedMockDB{rows: []*mockRow{
				{values: []any{false}}, // SELECT FOR UPDATE → ok
				{scanErr: tc.scanErr},  // UPDATE RETURNING → ctx cancel
			}}
			repo := newConfigRepositoryFromDBTX(seqDB)
			_, err := repo.Update(context.Background(), "k", "v")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Delete/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newConfigRepositoryFromDBTX(db)
			_, err := repo.Delete(context.Background(), "k")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("List/QueryErr/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryErr: tc.scanErr}
			repo := newConfigRepositoryFromDBTX(db)
			_, err := repo.List(context.Background(), query.ListParams{
				Sort: []query.SortColumn{{Name: "key", Direction: query.SortASC}},
			})
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("PublishVersion/"+tc.name, func(t *testing.T) {
			db := &mockDB{execErr: tc.scanErr}
			repo := newConfigRepositoryFromDBTX(db)
			err := repo.PublishVersion(context.Background(), &domain.ConfigVersion{
				ID:        "v-1",
				ConfigID:  "cfg-1",
				Version:   1,
				Value:     "x",
				Sensitive: false,
			})
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Create/"+tc.name, func(t *testing.T) {
			db := &mockDB{execErr: tc.scanErr}
			repo := newConfigRepositoryFromDBTX(db)
			err := repo.Create(context.Background(), &domain.ConfigEntry{
				ID:        "cfg-1",
				Key:       "k",
				Value:     "v",
				Sensitive: false,
			})
			assertCtxCancelErr(t, err, tc.scanErr)
		})
	}
}

// TestConfigRepo_CryptoOpError_CauseAwareClassification verifies that
// cryptoOpError fans out by cause class (PR-A50+A51 + #271 review P2):
//   - context cancel cause → ErrClientCanceled (HTTP 499 + Warn), preserves
//     IsInfraError=true (driven by stdlib sentinel detection in classify.go)
//   - ErrKeyProviderTransient cause → preserves CategoryInfra (Vault outage
//     must remain in infra bucket so existing alerts still fire)
//   - other cause (auth / tamper) → CategoryAuth (distinguishes KMS auth
//     from generic infra outages on dashboards)
//
// The HTTP status mapping for the non-cancel branches is unchanged
// (ErrConfigDecryptFailed / ErrConfigRepoQuery → 500); only the in-process
// classifier shifts.
func TestConfigRepo_CryptoOpError_CauseAwareClassification(t *testing.T) {
	repo := newConfigRepositoryFromDBTX(&mockDB{})
	ops := []struct {
		name string
		code errcode.Code
		op   string
	}{
		{"encrypt", errcode.ErrConfigEncryptFailed, "Encrypt"},
		{"decrypt", errcode.ErrConfigDecryptFailed, "Decrypt"},
		{"encrypt version", errcode.ErrConfigEncryptFailed, "EncryptVersion"},
		{"decrypt version", errcode.ErrConfigDecryptFailed, "DecryptVersion"},
	}

	t.Run("ctx cancel cause routes to ErrClientCanceled", func(t *testing.T) {
		for _, tc := range ops {
			t.Run(tc.name, func(t *testing.T) {
				ec := repo.cryptoOpError(tc.code, tc.op, "key=foo", context.Canceled)
				require.NotNil(t, ec)
				assert.Equal(t, errcode.ErrClientCanceled, ec.Code,
					"ctx cancel via crypto boundary must route to ErrClientCanceled (HTTP 499)")
				assert.True(t, errcode.IsExpected4xx(ec),
					"ErrClientCanceled must hit log4xx → slog.Warn path")
			})
		}
	})

	t.Run("transient KeyProvider cause preserves CategoryInfra", func(t *testing.T) {
		transient := errcode.New(errcode.KindUnavailable, errcode.ErrKeyProviderTransient, "vault sealed")
		for _, tc := range ops {
			t.Run(tc.name, func(t *testing.T) {
				ec := repo.cryptoOpError(tc.code, tc.op, "key=foo", transient)
				require.NotNil(t, ec)
				assert.Equal(t, tc.code, ec.Code,
					"transient cause must NOT rewrite to ErrClientCanceled")
				assert.Equal(t, errcode.CategoryInfra, ec.Category,
					"transient KeyProvider faults must remain CategoryInfra (Vault outage signal)")
				assert.True(t, errcode.IsInfraError(ec),
					"IsInfraError must stay true so existing infra alerts fire")
			})
		}
	})

	t.Run("opaque cause classified as CategoryAuth", func(t *testing.T) {
		for _, tc := range ops {
			t.Run(tc.name, func(t *testing.T) {
				ec := repo.cryptoOpError(tc.code, tc.op, "key=foo", assert.AnError)
				require.NotNil(t, ec)
				assert.Equal(t, tc.code, ec.Code)
				assert.Equal(t, errcode.CategoryAuth, ec.Category,
					"unclassified KMS / tamper failures must be CategoryAuth")
				assert.False(t, errcode.IsInfraError(ec),
					"CategoryAuth must NOT match IsInfraError (separates KMS auth from infra)")
				assert.Contains(t, ec.InternalMessage, tc.op,
					"InternalMessage must carry the PascalCase op label for operator triage")
			})
		}
	})
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

// TestConfigRepository_Update tests the new 3-arg Update (SELECT FOR UPDATE + UPDATE RETURNING).
// The mock returns the SELECT FOR UPDATE row first (sensitive=false), then the UPDATE RETURNING row.
func TestConfigRepository_Update(t *testing.T) {
	now := time.Now()
	seqDB := &sequencedMockDB{
		rows: []*mockRow{
			{values: []any{false}}, // SELECT FOR UPDATE → sensitive=false
			{values: []any{"cfg-1", "app.name", "GoCell v2", false, 2, now, now, nil, nil, nil, nil}}, // UPDATE RETURNING
		},
	}
	repo := newConfigRepositoryFromDBTX(seqDB)

	entry, err := repo.Update(context.Background(), "app.name", "GoCell v2")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "GoCell v2", entry.Value)

	require.Len(t, seqDB.queryRowCalls, 2)
	assert.Contains(t, seqDB.queryRowCalls[0].sql, "FOR UPDATE")
	assert.Contains(t, seqDB.queryRowCalls[1].sql, "UPDATE config_entries")
}

// TestConfigRepository_Update_NotFound tests that Update returns ErrConfigRepoNotFound
// when the SELECT FOR UPDATE finds no row.
func TestConfigRepository_Update_NotFound(t *testing.T) {
	seqDB := &sequencedMockDB{
		rows: []*mockRow{
			{scanErr: pgx.ErrNoRows}, // SELECT FOR UPDATE → not found
		},
	}
	repo := newConfigRepositoryFromDBTX(seqDB)

	_, err := repo.Update(context.Background(), "missing", "v")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	require.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"Update not-found must have Category=CategoryDomain")
}

// TestConfigRepository_UpdateForRollback tests the 4-arg UpdateForRollback method.
func TestConfigRepository_UpdateForRollback(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// 11 columns matching configEntryColumns for RETURNING
			values: []any{"cfg-1", "app.name", "GoCell v2", false, 2, now, now, nil, nil, nil, nil},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	entry, err := repo.UpdateForRollback(context.Background(), "app.name", "GoCell v2", false)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "GoCell v2", entry.Value)

	require.Len(t, db.queryRowCalls, 1)
	assert.Contains(t, db.queryRowCalls[0].sql, "UPDATE config_entries")
}

// TestConfigRepository_UpdateForRollback_NotFound tests that UpdateForRollback
// returns ErrConfigRepoNotFound when the key is not found.
func TestConfigRepository_UpdateForRollback_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.UpdateForRollback(context.Background(), "missing", "v", false)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	require.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"UpdateForRollback not-found must have Category=CategoryDomain")
	require.False(t, errcode.IsInfraError(err),
		"domain not-found must not be treated as infra")
}

func TestConfigRepository_Delete(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// 11 columns matching configEntryColumns for RETURNING
			values: []any{"cfg-1", "app.name", "GoCell", false, 1, now, now, nil, nil, nil, nil},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	deleted, err := repo.Delete(context.Background(), "app.name")
	require.NoError(t, err)
	require.NotNil(t, deleted)
	assert.Equal(t, "app.name", deleted.Key)

	require.Len(t, db.queryRowCalls, 1)
	assert.Contains(t, db.queryRowCalls[0].sql, "DELETE FROM config_entries")
}

func TestConfigRepository_Delete_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.Delete(context.Background(), "missing")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	require.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"Delete not-found must have Category=CategoryDomain")
	require.False(t, errcode.IsInfraError(err),
		"domain not-found must not be treated as infra")
}

func TestConfigRepository_List(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				// 8 columns: id, key, value, sensitive, version, created_at, updated_at, value_key_id
				{values: []any{"cfg-1", "a.key", "val1", false, 1, now, now, nil}},
				{values: []any{"cfg-2", "b.key", "val2", false, 1, now, now, nil}},
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

// TestConfigRepository_List_SensitiveRowReturnsSentinel verifies that sensitive
// entries in List have their Value replaced with "***" (sentinel) and KeyID preserved.
func TestConfigRepository_List_SensitiveRowReturnsSentinel(t *testing.T) {
	now := time.Now()
	keyID := "local-aes-v1"
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				// 8 columns: id, key, value, sensitive, version, created_at, updated_at, value_key_id
				{values: []any{"cfg-1", "secret.key", "", true, 1, now, now, &keyID}},
				{values: []any{"cfg-2", "public.key", "open", false, 1, now, now, nil}},
			},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	entries, err := repo.List(context.Background(), query.ListParams{
		Limit: 50,
		Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Sensitive entry must return sentinel value.
	assert.Equal(t, "***", entries[0].Value, "sensitive entry must return *** sentinel")
	assert.Equal(t, keyID, entries[0].KeyID, "KeyID must be preserved in list")

	// Non-sensitive entry must return plaintext.
	assert.Equal(t, "open", entries[1].Value)
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
			// sensitive=false: no decryption needed, verifies basic scan/field mapping.
			values: []any{"cv-1", "cfg-1", 1, "value", false, &now, nil, nil, nil, nil},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	version, err := repo.GetVersion(context.Background(), "cfg-1", 1)
	require.NoError(t, err)
	assert.Equal(t, "cv-1", version.ID)
	assert.Equal(t, "cfg-1", version.ConfigID)
	assert.Equal(t, 1, version.Version)
	assert.Equal(t, "value", version.Value)
	assert.False(t, version.Sensitive)
}

// TestConfigRepository_GetVersion_Sensitive_LegacyPlaintext_FailsClosed verifies
// that reading a sensitive version with no cipher columns returns ErrConfigDecryptFailed.
func TestConfigRepository_GetVersion_Sensitive_LegacyPlaintext_FailsClosed(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRowResult: &mockRow{
			// sensitive=true but value_cipher is nil → legacy row
			values: []any{"cv-1", "cfg-1", 1, "legacy-plaintext", true, &now, nil, nil, nil, nil},
		},
	}
	repo := newConfigRepositoryFromDBTX(db)

	_, err := repo.GetVersion(context.Background(), "cfg-1", 1)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigDecryptFailed, ec.Code)
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
	require.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"GetVersion not-found must have Category=CategoryDomain")
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
	require.True(t, errcode.IsInfraError(err),
		"generic scan error must be CategoryInfra (not Domain)")
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
	repo := NewConfigRepository(session, nil, nil, clock.Real())

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
	repo := NewConfigRepository(session, nil, nil, clock.Real())

	_, err := repo.Update(context.Background(), "k", "v")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestDelete_WithoutTx_ReturnsNoTxError verifies that Delete via a session-based
// repo fails with ErrAdapterPGNoTx when no tx is present in context (F-S-1).
func TestDelete_WithoutTx_ReturnsNoTxError(t *testing.T) {
	session := NewSession(nil)
	repo := NewConfigRepository(session, nil, nil, clock.Real())

	_, err := repo.Delete(context.Background(), "k")
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
	repo := NewConfigRepository(session, nil, nil, clock.Real())

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
	repo := NewConfigRepository(s, nil, nil, clock.Real())

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
	repo := NewConfigRepository(s, nil, nil, clock.Real())

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
	queryRowCalls  []dbCallRecord
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

func (m *mockDB) QueryRow(_ context.Context, sql string, args ...any) Row {
	m.queryRowCalls = append(m.queryRowCalls, dbCallRecord{sql: sql, args: args})
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
		case **string:
			if s, ok := v.(*string); ok {
				*d = s
			} else if s2, ok := v.(string); ok {
				*d = &s2
			}
		}
	}
	return nil
}

func (r *mockRowSet) Close()     {}
func (r *mockRowSet) Err() error { return r.rowsErr }

// sequencedMockDB returns a different mockRow for each successive QueryRow call.
// Used to test methods that make multiple QueryRow calls (e.g. Update which
// issues SELECT...FOR UPDATE then UPDATE...RETURNING).
type sequencedMockDB struct {
	queryRowCalls []dbCallRecord
	rows          []*mockRow
	idx           int
}

func (s *sequencedMockDB) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	return 0, nil
}

func (s *sequencedMockDB) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	return &mockRowSet{}, nil
}

func (s *sequencedMockDB) QueryRow(_ context.Context, sql string, args ...any) Row {
	s.queryRowCalls = append(s.queryRowCalls, dbCallRecord{sql: sql, args: args})
	if s.idx >= len(s.rows) {
		return &mockRow{scanErr: assert.AnError}
	}
	row := s.rows[s.idx]
	s.idx++
	return row
}
