// Package postgres provides a PostgreSQL implementation of config-core ports.
// It does NOT import adapters/postgres — it defines its own DBTX interface
// to match pgx.Tx / pgxpool.Pool, keeping the cell decoupled from the adapter layer.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/jackc/pgx/v5"
)

// DBTX abstracts the database operations needed by ConfigRepository.
// Both pgxpool.Pool and pgx.Tx satisfy this interface.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// Rows abstracts a query result set.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// Row abstracts a single-row result.
type Row interface {
	Scan(dest ...any) error
}

// Compile-time interface check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

// ConfigRepository implements ports.ConfigRepository using PostgreSQL.
type ConfigRepository struct {
	db          DBTX     // test-only: set by newConfigRepositoryFromDBTX (unexported helper in test file)
	session     *Session // production path: resolves ambient tx via persistence.TxCtxKey
	transformer crypto.ValueTransformer
}

// NewConfigRepository creates a ConfigRepository that resolves the ambient
// pgx.Tx from the context on each call, enabling transactional participation
// via persistence.TxCtxKey. Session is the sole production entry point;
// use the unexported newConfigRepositoryFromDBTX in tests.
//
// Requires migrations 001–008 to be applied first (see adapters/postgres/migrations/).
func NewConfigRepository(s *Session, tr crypto.ValueTransformer) *ConfigRepository {
	return &ConfigRepository{session: s, transformer: tr}
}

// resolveDB returns the DBTX to use for read calls. When a Session is
// configured it resolves the ambient transaction from ctx (falling back to
// pool for non-transactional reads); otherwise the fixed DBTX is used
// (unit-test path).
func (r *ConfigRepository) resolveDB(ctx context.Context) DBTX {
	if r.session != nil {
		return r.session.resolve(ctx)
	}
	return r.db
}

// resolveWriteDB returns the DBTX for write calls. When a Session is
// configured it requires a tx in ctx (L2 atomicity guarantee); otherwise
// falls back to the fixed DBTX (unit-test path).
func (r *ConfigRepository) resolveWriteDB(ctx context.Context) (DBTX, error) {
	if r.session != nil {
		return r.session.resolveWrite(ctx)
	}
	return r.db, nil
}

// encryptValue encrypts value for a sensitive entry using the transformer.
// Returns (ciphertext, keyID, nonce, edk) or error.
func (r *ConfigRepository) encryptValue(ctx context.Context, key, value string) (ct []byte, keyID string, nonce, edk []byte, err error) {
	if r.transformer == nil {
		return nil, "", nil, nil, errcode.New(errcode.ErrConfigKeyMissing,
			"config repo: no ValueTransformer configured for sensitive entry")
	}
	aad := crypto.AADForConfig("config-core", key)
	ct, keyID, nonce, edk, err = r.transformer.Encrypt(ctx, []byte(value), aad)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("config repo: encrypt value for key %s: %w", key, err)
	}
	return ct, keyID, nonce, edk, nil
}

// decryptValue decrypts a cipher-column tuple for a sensitive entry.
// Fail-closed: returns ErrConfigDecryptFailed on any error.
func (r *ConfigRepository) decryptValue(ctx context.Context, key string, ct []byte, keyID string, nonce, edk []byte) (string, error) {
	if r.transformer == nil {
		return "", errcode.New(errcode.ErrConfigDecryptFailed,
			"config repo: no ValueTransformer configured, cannot decrypt sensitive value")
	}
	aad := crypto.AADForConfig("config-core", key)
	pt, err := r.transformer.Decrypt(ctx, ct, keyID, nonce, edk, aad)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrConfigDecryptFailed,
			fmt.Sprintf("config repo: decrypt failed for key %s", key), err)
	}
	return string(pt), nil
}

// Create inserts a new config entry.
// For sensitive=true: encrypts value and writes cipher columns; value column is set to "".
// For sensitive=false: writes plaintext value; cipher columns are NULL.
func (r *ConfigRepository) Create(ctx context.Context, entry *domain.ConfigEntry) error {
	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = now
	}

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return err
	}

	if entry.Sensitive {
		ct, keyID, nonce, edk, encErr := r.encryptValue(ctx, entry.Key, entry.Value)
		if encErr != nil {
			return encErr
		}
		const q = `INSERT INTO config_entries
			(id, key, value, sensitive, version, created_at, updated_at,
			 value_cipher, value_key_id, value_edk, value_nonce)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
		_, err = db.Exec(ctx, q,
			entry.ID, entry.Key, "", entry.Sensitive, entry.Version,
			entry.CreatedAt, entry.UpdatedAt,
			ct, keyID, edk, nonce,
		)
	} else {
		const q = `INSERT INTO config_entries
			(id, key, value, sensitive, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`
		_, err = db.Exec(ctx, q,
			entry.ID, entry.Key, entry.Value, entry.Sensitive, entry.Version,
			entry.CreatedAt, entry.UpdatedAt,
		)
	}
	if err != nil {
		return errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("config repo: create failed for key %s", entry.Key), err)
	}
	return nil
}

// GetByKey retrieves a config entry by key with transparent decryption for
// sensitive entries. Sets entry.Stale=true when keyID != current active key.
func (r *ConfigRepository) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	const q = `SELECT id, key, value, sensitive, version, created_at, updated_at,
		value_cipher, value_key_id, value_edk, value_nonce
		FROM config_entries WHERE key = $1`

	row := r.resolveDB(ctx).QueryRow(ctx, q, key)

	var (
		e           domain.ConfigEntry
		valueCipher []byte
		valueKeyID  *string
		valueEDK    []byte
		valueNonce  []byte
	)
	if err := row.Scan(
		&e.ID, &e.Key, &e.Value, &e.Sensitive, &e.Version, &e.CreatedAt, &e.UpdatedAt,
		&valueCipher, &valueKeyID, &valueEDK, &valueNonce,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &errcode.Error{
				Code:            errcode.ErrConfigRepoNotFound,
				Message:         "config not found",
				InternalMessage: fmt.Sprintf("config repo: GetByKey miss key=%s", key),
				Cause:           err,
			}
		}
		return nil, &errcode.Error{
			Code:            errcode.ErrConfigRepoQuery,
			Message:         "config repo query failed",
			InternalMessage: fmt.Sprintf("config repo: GetByKey scan error key=%s", key),
			Cause:           err,
		}
	}

	// Fail-closed enforcement for sensitive entries.
	if e.Sensitive {
		if len(valueCipher) == 0 || valueKeyID == nil || *valueKeyID == "" {
			// Legacy plaintext row: sensitive=true but value_cipher IS NULL.
			// Block read until plaintext_migration completes to enforce
			// fail-closed semantics — never return plaintext from unencrypted rows.
			return nil, errcode.New(errcode.ErrConfigDecryptFailed,
				"sensitive value is in legacy plaintext format; run plaintext_migration tool before reading")
		}
		plain, err := r.decryptValue(ctx, key, valueCipher, *valueKeyID, valueNonce, valueEDK)
		if err != nil {
			return nil, err
		}
		e.Value = plain
		e.KeyID = *valueKeyID

		// Staleness check: if the stored keyID differs from current, mark stale.
		if r.transformer != nil {
			currentID := r.currentKeyID(ctx)
			if currentID != "" && currentID != *valueKeyID {
				e.Stale = true
			}
		}
	}

	return &e, nil
}

// currentKeyID returns the ID of the current key from the transformer.
// Returns "" if the transformer does not support key introspection or fails.
func (r *ConfigRepository) currentKeyID(ctx context.Context) string {
	type hasCurrent interface {
		CurrentKeyID(ctx context.Context) (string, error)
	}
	if c, ok := r.transformer.(hasCurrent); ok {
		id, err := c.CurrentKeyID(ctx)
		if err != nil {
			return ""
		}
		return id
	}
	return ""
}

// Update updates an existing config entry.
// For sensitive=true: re-encrypts value and writes cipher columns.
func (r *ConfigRepository) Update(ctx context.Context, entry *domain.ConfigEntry) error {
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return err
	}

	var affected int64
	if entry.Sensitive {
		ct, keyID, nonce, edk, encErr := r.encryptValue(ctx, entry.Key, entry.Value)
		if encErr != nil {
			return encErr
		}
		const q = `UPDATE config_entries
			SET value = $1, sensitive = $2, version = $3, updated_at = $4,
			    value_cipher = $5, value_key_id = $6, value_edk = $7, value_nonce = $8
			WHERE key = $9`
		affected, err = db.Exec(ctx, q,
			"", entry.Sensitive, entry.Version, entry.UpdatedAt,
			ct, keyID, edk, nonce, entry.Key,
		)
	} else {
		const q = `UPDATE config_entries
			SET value = $1, sensitive = $2, version = $3, updated_at = $4,
			    value_cipher = NULL, value_key_id = NULL, value_edk = NULL, value_nonce = NULL
			WHERE key = $5`
		affected, err = db.Exec(ctx, q,
			entry.Value, entry.Sensitive, entry.Version, entry.UpdatedAt, entry.Key,
		)
	}
	if err != nil {
		return errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("config repo: update failed for key %s", entry.Key), err)
	}
	if affected == 0 {
		return errcode.Safe(errcode.ErrConfigRepoNotFound,
			"config not found",
			fmt.Sprintf("config repo: Update miss key=%s", entry.Key))
	}
	return nil
}

// Delete removes a config entry by key.
func (r *ConfigRepository) Delete(ctx context.Context, key string) error {
	const q = `DELETE FROM config_entries WHERE key = $1`

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return err
	}
	affected, err := db.Exec(ctx, q, key)
	if err != nil {
		return errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("config repo: delete failed for key %s", key), err)
	}
	if affected == 0 {
		return errcode.Safe(errcode.ErrConfigRepoNotFound,
			"config not found",
			fmt.Sprintf("config repo: Delete miss key=%s", key))
	}
	return nil
}

// sensitiveListSentinel is the placeholder value returned by List for sensitive entries.
// List does not decrypt sensitive values — callers use GetByKey for plaintext access.
const sensitiveListSentinel = "***"

// applySensitiveListSentinel redacts the value field of a sensitive list entry
// and preserves key metadata (KeyID, Stale) for informational purposes.
func applySensitiveListSentinel(e *domain.ConfigEntry, valueKeyID *string, currentID string) {
	e.Value = sensitiveListSentinel
	if valueKeyID == nil {
		return
	}
	e.KeyID = *valueKeyID
	if currentID != "" && currentID != *valueKeyID {
		e.Stale = true
	}
}

// List retrieves config entries with keyset cursor pagination.
// Requires composite index: CREATE INDEX idx_config_entries_key_id ON config_entries (key ASC, id ASC)
//
// Sensitive entries: List does NOT decrypt values. Instead, the Value field is
// set to "***" (sentinel) and KeyID / Stale are preserved from the cipher columns.
// Callers must use GetByKey to retrieve the decrypted plaintext for a specific entry.
//
// This design avoids bulk decryption on list operations and prevents accidental
// exposure of sensitive values in list responses.
func (r *ConfigRepository) List(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
	b := query.NewBuilder()
	b.Append("SELECT id, key, value, sensitive, version, created_at, updated_at, value_key_id FROM config_entries WHERE 1=1")

	if err := query.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: keyset build failed", err)
	}

	sql, args := b.Build()
	rows, err := r.resolveDB(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: list failed", err)
	}
	defer rows.Close()

	currentID := r.currentKeyID(ctx)
	var entries []*domain.ConfigEntry
	for rows.Next() {
		var (
			e          domain.ConfigEntry
			valueKeyID *string
		)
		if err := rows.Scan(&e.ID, &e.Key, &e.Value, &e.Sensitive, &e.Version, &e.CreatedAt, &e.UpdatedAt, &valueKeyID); err != nil {
			return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: scan failed", err)
		}
		if e.Sensitive {
			applySensitiveListSentinel(&e, valueKeyID, currentID)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: rows error", err)
	}

	return entries, nil
}

// PublishVersion inserts a config version record.
// For sensitive=true: encrypts value and writes cipher columns.
func (r *ConfigRepository) PublishVersion(ctx context.Context, version *domain.ConfigVersion) error {
	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return err
	}

	if version.Sensitive {
		ct, keyID, nonce, edk, encErr := r.encryptValue(ctx, version.ConfigID, version.Value)
		if encErr != nil {
			return encErr
		}
		const q = `INSERT INTO config_versions
			(id, config_id, version, value, sensitive, published_at,
			 value_cipher, value_key_id, value_edk, value_nonce)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
		_, err = db.Exec(ctx, q,
			version.ID, version.ConfigID, version.Version,
			"", version.Sensitive, version.PublishedAt,
			ct, keyID, edk, nonce,
		)
	} else {
		const q = `INSERT INTO config_versions
			(id, config_id, version, value, sensitive, published_at)
			VALUES ($1, $2, $3, $4, $5, $6)`
		_, err = db.Exec(ctx, q,
			version.ID, version.ConfigID, version.Version,
			version.Value, version.Sensitive, version.PublishedAt,
		)
	}
	if err != nil {
		return errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("config repo: publish version failed for config %s v%d",
				version.ConfigID, version.Version), err)
	}
	return nil
}

// GetVersion retrieves a specific config version with transparent decryption.
func (r *ConfigRepository) GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error) {
	const q = `SELECT id, config_id, version, value, sensitive, published_at,
		value_cipher, value_key_id, value_edk, value_nonce
		FROM config_versions WHERE config_id = $1 AND version = $2`

	row := r.resolveDB(ctx).QueryRow(ctx, q, configID, version)

	var (
		v           domain.ConfigVersion
		valueCipher []byte
		valueKeyID  *string
		valueEDK    []byte
		valueNonce  []byte
	)
	if err := row.Scan(
		&v.ID, &v.ConfigID, &v.Version, &v.Value, &v.Sensitive, &v.PublishedAt,
		&valueCipher, &valueKeyID, &valueEDK, &valueNonce,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &errcode.Error{
				Code:            errcode.ErrConfigRepoNotFound,
				Message:         "config version not found",
				InternalMessage: fmt.Sprintf("config repo: GetVersion miss config_id=%s version=%d", configID, version),
				Cause:           err,
			}
		}
		return nil, &errcode.Error{
			Code:            errcode.ErrConfigRepoQuery,
			Message:         "config repo query failed",
			InternalMessage: fmt.Sprintf("config repo: GetVersion scan error config_id=%s version=%d", configID, version),
			Cause:           err,
		}
	}

	// Fail-closed enforcement for sensitive versions.
	if v.Sensitive {
		if len(valueCipher) == 0 || valueKeyID == nil || *valueKeyID == "" {
			// Legacy plaintext version row: block read until plaintext_migration completes.
			return nil, errcode.New(errcode.ErrConfigDecryptFailed,
				"sensitive version is in legacy plaintext format; run plaintext_migration tool before reading")
		}
		plain, err := r.decryptValue(ctx, v.ConfigID, valueCipher, *valueKeyID, valueNonce, valueEDK)
		if err != nil {
			return nil, err
		}
		v.Value = plain
	}

	return &v, nil
}
