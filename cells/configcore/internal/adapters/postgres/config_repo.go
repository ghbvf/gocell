// Package postgres provides a PostgreSQL implementation of configcore ports.
// It does NOT import adapters/postgres — it defines its own DBTX interface
// to match pgx.Tx / pgxpool.Pool, keeping the cell decoupled from the adapter layer.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	configcrypto "github.com/ghbvf/gocell/cells/configcore/internal/crypto"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/jackc/pgx/v5"
)

// cryptoOpError constructs a uniform *errcode.Error for encrypt/decrypt
// operation failures, classifying the cause to preserve dashboards' ability
// to distinguish three signal sources that the ValueTransformer chain may
// surface (in order of detection):
//
//  1. Context cancellation (`context.Canceled` / `DeadlineExceeded`) →
//     ErrClientCanceled (HTTP 499 + slog.Warn). Routed via the canonical
//     ctxcancel helper so client-direction signals never pollute 5xx SLOs,
//     even when surfaced through the crypto boundary.
//  2. Transient KeyProvider faults (`ErrKeyProviderTransient`: Vault sealed,
//     rate-limited, request timeout) → preserve CategoryInfra. These are
//     real infrastructure outages and must remain in the infra bucket so
//     `IsInfraError` predicates and Vault-outage alerts still fire.
//  3. Default → CategoryAuth. The remaining cases are KeyProvider
//     authorisation failures (KMS access denied, key rotation race,
//     ciphertext keyID mismatch) and AES-GCM tamper detection — distinct
//     from infra so dashboards can route KMS-auth alerts separately.
//
// Public Message stays a generic descriptor; InternalMessage embeds the
// identifier (config key or configID) for internal triage only. The HTTP
// status mapping is driven independently by codeToStatus
// (ErrConfigEncryptFailed → 500, ErrConfigDecryptFailed → 500,
// ErrConfigRepoQuery → 500, ErrClientCanceled → 499); only the in-process classifier shifts.
//
// ref: google/tink aead/subtle/aes_gcm.go — symmetric crypto errors do not
// carry key identifiers in Error() strings.
// ref: FiloSottile/age age.go — encrypt errors use recipient index, not key.
func (r *ConfigRepository) cryptoOpError(code errcode.Code, op, identifier string, cause error) *errcode.Error {
	if cancelErr := ctxcancel.Wrap(cause, op, identifier); cancelErr != nil {
		return cancelErr
	}
	category := errcode.CategoryAuth
	if errcode.IsTransient(cause) {
		category = errcode.CategoryInfra
	}
	return &errcode.Error{
		Code:            code,
		Message:         fmt.Sprintf("config repo: %s failed", op),
		InternalMessage: fmt.Sprintf("config repo: %s failed (%s)", op, identifier),
		Cause:           cause,
		Category:        category,
	}
}

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

// cellID is the canonical cell identifier used by this adapter to construct
// the AAD for AES-GCM-authenticated sensitive value encryption. It must match
// the configcore cell's ID; a mismatch makes every encrypted value
// undecryptable. Declared as a package constant (single source of truth)
// so encryptValue / decryptValue / plaintext_migration all derive AAD
// identically.
const cellID = "configcore"

// Compile-time interface check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

// ConfigRepository implements ports.ConfigRepository using PostgreSQL.
type ConfigRepository struct {
	db          DBTX     // test-only: set by newConfigRepositoryFromDBTX (unexported helper in test file)
	session     *Session // production path: resolves ambient tx via persistence.TxCtxKey
	transformer kcrypto.ValueTransformer
	logger      *slog.Logger
	// onStaleCipher is an optional callback invoked when a stale-key value is
	// detected during a read. Callers may wire this to a prometheus counter:
	//   repo.onStaleCipher = func(_, _, _ string) { staleCipherTotal.Inc() }
	// The callback receives (key, storedKeyID, currentKeyID). When nil, it is
	// skipped; slog.Warn is always emitted regardless.
	onStaleCipher func(key, storedKeyID, currentKeyID string)
}

// ConfigRepoOption configures optional behaviour on ConfigRepository.
type ConfigRepoOption func(*ConfigRepository)

// WithOnStaleCipher sets a callback invoked when a stale-key value is detected
// during a read. Callers may wire this to a prometheus counter:
//
//	WithOnStaleCipher(func(key, storedKeyID, currentKeyID string) {
//	    staleCipherTotal.Inc()
//	})
//
// The callback receives (key, storedKeyID, currentKeyID). When nil, it is
// skipped; slog.Warn is always emitted regardless.
func WithOnStaleCipher(fn func(key, storedKeyID, currentKeyID string)) ConfigRepoOption {
	return func(r *ConfigRepository) {
		r.onStaleCipher = fn
	}
}

// NewConfigRepository creates a ConfigRepository that resolves the ambient
// pgx.Tx from the context on each call, enabling transactional participation
// via persistence.TxCtxKey. Session is the sole production entry point;
// use the unexported newConfigRepositoryFromDBTX in tests.
//
// If logger is nil, slog.Default() is used.
//
// Requires migrations 001–010 to be applied first (see adapters/postgres/migrations/).
func NewConfigRepository(s *Session, tr kcrypto.ValueTransformer, logger *slog.Logger, opts ...ConfigRepoOption) *ConfigRepository {
	if logger == nil {
		logger = slog.Default()
	}
	r := &ConfigRepository{session: s, transformer: tr, logger: logger}
	for _, opt := range opts {
		opt(r)
	}
	return r
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
	aad := configcrypto.AADForConfig(cellID, key)
	ct, keyID, nonce, edk, err = r.transformer.Encrypt(ctx, []byte(value), aad)
	if err != nil {
		return nil, "", nil, nil, r.cryptoOpError(errcode.ErrConfigEncryptFailed, "Encrypt", "key="+key, err)
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
	aad := configcrypto.AADForConfig(cellID, key)
	pt, err := r.transformer.Decrypt(ctx, ct, keyID, nonce, edk, aad)
	if err != nil {
		return "", r.cryptoOpError(errcode.ErrConfigDecryptFailed, "Decrypt", "key="+key, err)
	}
	return string(pt), nil
}

// encryptVersionValue encrypts value for a sensitive config version.
// Uses AADForVersion (not AADForConfig) so the AAD domain is distinct from
// config entries — prevents cross-field ciphertext replay between the two tables.
// configID is the UUID primary key from config_entries.
func (r *ConfigRepository) encryptVersionValue(ctx context.Context, configID, value string) (ct []byte, keyID string, nonce, edk []byte, err error) {
	if r.transformer == nil {
		return nil, "", nil, nil, errcode.New(errcode.ErrConfigKeyMissing,
			"config repo: no ValueTransformer configured for sensitive version")
	}
	aad := configcrypto.AADForVersion(cellID, configID)
	ct, keyID, nonce, edk, err = r.transformer.Encrypt(ctx, []byte(value), aad)
	if err != nil {
		return nil, "", nil, nil, r.cryptoOpError(errcode.ErrConfigEncryptFailed, "EncryptVersion", "config_id="+configID, err)
	}
	return ct, keyID, nonce, edk, nil
}

// decryptVersionValue decrypts a cipher-column tuple for a sensitive config version.
// Uses AADForVersion so the AAD matches the write path in encryptVersionValue.
// Fail-closed: returns ErrConfigDecryptFailed on any error.
func (r *ConfigRepository) decryptVersionValue(ctx context.Context, configID string, ct []byte, keyID string, nonce, edk []byte) (string, error) {
	if r.transformer == nil {
		return "", errcode.New(errcode.ErrConfigDecryptFailed,
			"config repo: no ValueTransformer configured, cannot decrypt sensitive version")
	}
	aad := configcrypto.AADForVersion(cellID, configID)
	pt, err := r.transformer.Decrypt(ctx, ct, keyID, nonce, edk, aad)
	if err != nil {
		return "", r.cryptoOpError(errcode.ErrConfigDecryptFailed, "DecryptVersion", "config_id="+configID, err)
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
		// NOTE: SQL param order (edk, nonce) differs from encryptValue return
		// order (nonce, edk). Matches column order: value_edk=$10, value_nonce=$11.
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
		if cancelErr := ctxcancel.Wrap(err, "Create", "key="+entry.Key); cancelErr != nil {
			return cancelErr
		}
		return &errcode.Error{
			Code:            errcode.ErrConfigRepoQuery,
			Message:         "config repo: create failed",
			InternalMessage: fmt.Sprintf("config repo: Create failed (key=%s)", entry.Key),
			Cause:           err,
			Category:        errcode.CategoryInfra,
		}
	}
	return nil
}

// configEntryColumns is the canonical column list for config_entries used by
// every SELECT/RETURNING projection in this file. Centralised so the column
// order stays in sync between GetByKey, Update RETURNING, and Delete RETURNING.
const configEntryColumns = "id, key, value, sensitive, version, created_at, updated_at, value_cipher, value_key_id, value_edk, value_nonce"

// listEntryColumns is the reduced column set for List operations. It excludes
// cipher payload columns (value_cipher, value_edk, value_nonce) because List
// does not decrypt sensitive values — it returns "***" sentinel instead.
// See applySensitiveListSentinel for the redaction logic.
const listEntryColumns = "id, key, value, sensitive, version, created_at, updated_at, value_key_id"

// scanConfigRow scans one row (order matches configEntryColumns) into a
// ConfigEntry plus the raw cipher tuple. The caller is responsible for
// decrypting the cipher tuple via decryptScannedEntry.
func scanConfigRow(row Row) (e *domain.ConfigEntry, valueCipher []byte, valueKeyID *string, valueEDK []byte, valueNonce []byte, err error) {
	var entry domain.ConfigEntry
	if scanErr := row.Scan(
		&entry.ID, &entry.Key, &entry.Value, &entry.Sensitive, &entry.Version,
		&entry.CreatedAt, &entry.UpdatedAt,
		&valueCipher, &valueKeyID, &valueEDK, &valueNonce,
	); scanErr != nil {
		return nil, nil, nil, nil, nil, scanErr
	}
	return &entry, valueCipher, valueKeyID, valueEDK, valueNonce, nil
}

// scanConfigOrMapError calls scanConfigRow and translates the three known
// failure modes into GoCell errcode.Error values:
//
//   - context.Canceled / context.DeadlineExceeded → ErrClientCanceled (HTTP 499 + slog.Warn).
//   - pgx.ErrNoRows  → ErrConfigRepoNotFound (domain not-found; maps to 404).
//   - anything else  → ErrConfigRepoQuery (infra failure; maps to 500).
//
// The ctx.Canceled guard is checked first (before pgx.ErrNoRows) to prevent
// context cancellation from being misclassified as a domain not-found
// condition (S15 ctx-cancel classification fix). Client cancellation now
// routes via pkg/ctxcancel → 499 + slog.Warn, distinct from 5xx
// infrastructure failures so 5xx error-rate SLOs stay clean.
//
// op is the method name used only in InternalMessage for operator
// debugging; key is the lookup key surfaced the same way. Uses
// pkg/ctxcancel.Wrap for the ctx-cancel branch — the canonical helper
// already handles 499 vs 504 split and slog routing.
func (r *ConfigRepository) scanConfigOrMapError(ctx context.Context, row Row, op, key string) (*domain.ConfigEntry, []byte, *string, []byte, []byte, error) {
	e, ct, keyID, edk, nonce, err := scanConfigRow(row)
	if err == nil {
		return e, ct, keyID, edk, nonce, nil
	}
	if infraErr := ctxcancel.Wrap(err, op, "key="+key); infraErr != nil {
		return nil, nil, nil, nil, nil, infraErr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil, nil, &errcode.Error{
			Code:            errcode.ErrConfigRepoNotFound,
			Message:         "config not found",
			InternalMessage: fmt.Sprintf("config repo: %s miss key=%s", op, key),
			Cause:           err,
			Category:        errcode.CategoryDomain,
		}
	}
	return nil, nil, nil, nil, nil, &errcode.Error{
		Code:            errcode.ErrConfigRepoQuery,
		Message:         "config repo query failed",
		InternalMessage: fmt.Sprintf("config repo: %s scan error key=%s", op, key),
		Cause:           err,
		Category:        errcode.CategoryInfra,
	}
}

// decryptScannedEntry applies fail-closed sensitive-value decryption and stale-key
// detection to an already-scanned ConfigEntry. For non-sensitive entries it is a
// no-op. The cipher tuple fields (ct, keyID, edk, nonce) are the raw values
// returned by scanConfigRow.
func (r *ConfigRepository) decryptScannedEntry(ctx context.Context, e *domain.ConfigEntry, ct []byte, keyID *string, nonce, edk []byte) error {
	if !e.Sensitive {
		return nil
	}
	if len(ct) == 0 || keyID == nil || *keyID == "" {
		// Legacy plaintext row: sensitive=true but value_cipher IS NULL.
		return errcode.New(errcode.ErrConfigDecryptFailed,
			"sensitive value is in legacy plaintext format; run plaintext_migration tool before reading")
	}
	plain, err := r.decryptValue(ctx, e.Key, ct, *keyID, nonce, edk)
	if err != nil {
		return err
	}
	e.Value = plain
	e.KeyID = *keyID

	// Staleness check: if the stored keyID differs from current, mark stale.
	if r.transformer != nil {
		currentID := r.currentKeyID(ctx)
		if currentID != "" && currentID != *keyID {
			e.Stale = true
			r.observeStaleCipher(ctx, e.Key, *keyID, currentID)
		}
	}
	return nil
}

// GetByKey retrieves a config entry by key with transparent decryption for
// sensitive entries. Sets entry.Stale=true when keyID != current active key.
func (r *ConfigRepository) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	const q = `SELECT ` + configEntryColumns + ` FROM config_entries WHERE key = $1`
	row := r.resolveDB(ctx).QueryRow(ctx, q, key)
	e, ct, keyID, edk, nonce, err := r.scanConfigOrMapError(ctx, row, "GetByKey", key)
	if err != nil {
		return nil, err
	}
	if err := r.decryptScannedEntry(ctx, e, ct, keyID, nonce, edk); err != nil {
		return nil, err
	}
	return e, nil
}

// observeStaleCipher fans a stale-cipher signal out to the log and metric
// planes with strict redaction asymmetry: the slog.Warn carries only the
// business key name (operators can correlate by domain identity), while the
// stored/current key IDs flow exclusively to the onStaleCipher callback that
// caller-wired Prometheus counters consume as bounded-cardinality labels.
// Cryptographic identifiers must not appear on the log plane.
func (r *ConfigRepository) observeStaleCipher(ctx context.Context, key, storedKeyID, currentKeyID string) {
	r.logger.WarnContext(ctx, "config value encrypted with stale key",
		slog.String("key", key),
	)
	if r.onStaleCipher != nil {
		r.onStaleCipher(key, storedKeyID, currentKeyID)
	}
}

// currentKeyID returns the ID of the current key from the transformer.
// Returns "" if the transformer does not support key introspection or fails.
// Discovery is via the optional kcrypto.CurrentKeyIDProvider extension
// interface — NoopTransformer does not implement it, so staleness is never
// computed for non-sensitive values.
func (r *ConfigRepository) currentKeyID(ctx context.Context) string {
	if c, ok := r.transformer.(kcrypto.CurrentKeyIDProvider); ok {
		id, err := c.CurrentKeyID(ctx)
		if err != nil {
			return ""
		}
		return id
	}
	return ""
}

// Update atomically sets value and increments version. Preserves the existing
// sensitive flag — reads it internally via SELECT...FOR UPDATE to eliminate
// any TOCTOU race on the sensitive flag. Callers do not need to pre-read the
// entry. Returns ErrConfigRepoNotFound if the key does not exist.
func (r *ConfigRepository) Update(ctx context.Context, key string, value string) (*domain.ConfigEntry, error) {
	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}

	// Lock the row and read the current sensitive flag atomically.
	const selectQ = `SELECT sensitive FROM config_entries WHERE key = $1 FOR UPDATE`
	var sensitive bool
	selectRow := db.QueryRow(ctx, selectQ, key)
	if scanErr := selectRow.Scan(&sensitive); scanErr != nil {
		if infraErr := ctxcancel.Wrap(scanErr, "Update", "key="+key); infraErr != nil {
			return nil, infraErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, &errcode.Error{
				Code:            errcode.ErrConfigRepoNotFound,
				Message:         "config not found",
				InternalMessage: fmt.Sprintf("config repo: Update miss key=%s", key),
				Cause:           scanErr,
				Category:        errcode.CategoryDomain,
			}
		}
		return nil, &errcode.Error{
			Code:            errcode.ErrConfigRepoQuery,
			Message:         "config repo query failed",
			InternalMessage: fmt.Sprintf("config repo: Update select-for-update error key=%s", key),
			Cause:           scanErr,
			Category:        errcode.CategoryInfra,
		}
	}

	return r.doUpdate(ctx, db, "Update", key, value, sensitive)
}

// UpdateForRollback atomically sets value AND sensitive, increments version.
// Used exclusively by configpublish.Rollback to restore a snapshot's sensitivity
// alongside its value. Returns ErrConfigRepoNotFound if the key does not exist.
func (r *ConfigRepository) UpdateForRollback(ctx context.Context, key string, value string, sensitive bool) (*domain.ConfigEntry, error) {
	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	return r.doUpdate(ctx, db, "UpdateForRollback", key, value, sensitive)
}

// doUpdate performs the actual UPDATE...RETURNING for both Update and
// UpdateForRollback. op identifies the calling method for InternalMessage context.
// sensitive is the resolved flag (read by caller or provided directly).
// For sensitive=true: re-encrypts value and writes cipher columns.
func (r *ConfigRepository) doUpdate(ctx context.Context, db DBTX, op, key string, value string, sensitive bool) (*domain.ConfigEntry, error) {
	var row Row
	if sensitive {
		ct, keyID, nonce, edk, encErr := r.encryptValue(ctx, key, value)
		if encErr != nil {
			return nil, encErr
		}
		// NOTE: SQL param order (edk, nonce) differs from encryptValue return
		// order (nonce, edk). Matches the column order: value_edk=$3, value_nonce=$4.
		const q = `UPDATE config_entries
			SET value = '', sensitive = true, version = version+1, updated_at = now(),
			    value_cipher = $1, value_key_id = $2, value_edk = $3, value_nonce = $4
			WHERE key = $5
			RETURNING ` + configEntryColumns
		row = db.QueryRow(ctx, q, ct, keyID, edk, nonce, key)
	} else {
		const q = `UPDATE config_entries
			SET value = $1, sensitive = false, version = version+1, updated_at = now(),
			    value_cipher = NULL, value_key_id = NULL, value_edk = NULL, value_nonce = NULL
			WHERE key = $2
			RETURNING ` + configEntryColumns
		row = db.QueryRow(ctx, q, value, key)
	}

	e, ct, keyID, edk, nonce, scanErr := r.scanConfigOrMapError(ctx, row, op, key)
	if scanErr != nil {
		return nil, scanErr
	}
	if err := r.decryptScannedEntry(ctx, e, ct, keyID, nonce, edk); err != nil {
		return nil, err
	}
	return e, nil
}

// Delete atomically removes a config entry by key and returns the deleted row
// via DELETE...RETURNING, enabling callers to publish a tombstone event without
// a separate pre-read.
func (r *ConfigRepository) Delete(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	const q = `DELETE FROM config_entries WHERE key = $1 RETURNING ` + configEntryColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(ctx, q, key)
	e, ct, keyID, edk, nonce, scanErr := r.scanConfigOrMapError(ctx, row, "Delete", key)
	if scanErr != nil {
		return nil, scanErr
	}
	if err := r.decryptScannedEntry(ctx, e, ct, keyID, nonce, edk); err != nil {
		return nil, err
	}
	return e, nil
}

// sensitiveListSentinel is the placeholder value returned by List for sensitive entries.
// List does not decrypt sensitive values — callers use GetByKey for plaintext access.
const sensitiveListSentinel = "***"

// applySensitiveListSentinel redacts the value field of a sensitive list entry,
// preserves key metadata (KeyID, Stale) for informational purposes, and emits
// observability signals (slog.Warn + optional onStaleCipher callback) when the
// stored keyID differs from the current active keyID.
//
// staleLogged tracks which stale keyIDs have already been logged in this List
// call to prevent log flooding when many entries share the same stale keyID.
// The onStaleCipher callback always fires for every stale entry (metric accuracy);
// only the slog.Warn is deduplicated per distinct keyID per List call.
func (r *ConfigRepository) applySensitiveListSentinel(ctx context.Context, e *domain.ConfigEntry, valueKeyID *string, currentID string, staleLogged map[string]bool) {
	e.Value = sensitiveListSentinel
	if valueKeyID == nil {
		return
	}
	e.KeyID = *valueKeyID
	if currentID != "" && currentID != *valueKeyID {
		e.Stale = true
		if r.onStaleCipher != nil {
			r.onStaleCipher(e.Key, *valueKeyID, currentID)
		}
		if !staleLogged[*valueKeyID] {
			staleLogged[*valueKeyID] = true
			// Dedup is keyed on the stale keyID (one warn per distinct keyID per
			// page) so the metric callback above continues to count every entry.
			// The log line itself surfaces the business key of the first occurrence
			// so operators can correlate without us emitting the keyID.
			r.logger.WarnContext(ctx, "config values encrypted with stale key (first occurrence in this List page)",
				slog.String("key", e.Key),
			)
		}
	}
}

// List retrieves config entries with keyset cursor pagination.
//
// Performance note: keyset pagination on `(key, id)` scans well in practice
// because (a) the existing primary-key unique index on `id` supports the tie-
// breaker and (b) the `key` column is typically low-cardinality for admin
// browsing. A dedicated `(key ASC, id ASC)` composite index can be added in a
// future migration if sort-heavy list traffic warrants it; it is intentionally
// not shipped in migration 010 to keep this PR's migration scope minimal
// (010 only adds the cipher columns — see docs/backlog.md).
//
// Sensitive entries: List does NOT decrypt values. Instead, the Value field is
// set to "***" (sentinel) and KeyID / Stale are preserved from the cipher columns.
// Callers must use GetByKey to retrieve the decrypted plaintext for a specific entry.
//
// This design avoids bulk decryption on list operations and prevents accidental
// exposure of sensitive values in list responses.
func (r *ConfigRepository) List(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
	b := query.NewBuilder()
	b.Append("SELECT " + listEntryColumns + " FROM config_entries WHERE 1=1")

	if err := query.AppendKeyset(b, params); err != nil {
		return nil, errcode.WrapInfra(errcode.ErrConfigRepoQuery, "config repo: keyset build failed", err)
	}

	sql, args := b.Build()
	rows, err := r.resolveDB(ctx).Query(ctx, sql, args...)
	if err != nil {
		if cancelErr := ctxcancel.Wrap(err, "List", ""); cancelErr != nil {
			return nil, cancelErr
		}
		return nil, errcode.WrapInfra(errcode.ErrConfigRepoQuery, "config repo: list failed", err)
	}
	defer rows.Close()

	currentID := r.currentKeyID(ctx)
	staleLogged := make(map[string]bool)
	var entries []*domain.ConfigEntry
	for rows.Next() {
		var (
			e          domain.ConfigEntry
			valueKeyID *string
		)
		if err := rows.Scan(&e.ID, &e.Key, &e.Value, &e.Sensitive, &e.Version, &e.CreatedAt, &e.UpdatedAt, &valueKeyID); err != nil {
			if cancelErr := ctxcancel.Wrap(err, "List", ""); cancelErr != nil {
				return nil, cancelErr
			}
			return nil, errcode.WrapInfra(errcode.ErrConfigRepoQuery, "config repo: scan failed", err)
		}
		if e.Sensitive {
			r.applySensitiveListSentinel(ctx, &e, valueKeyID, currentID, staleLogged)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		if cancelErr := ctxcancel.Wrap(err, "List", ""); cancelErr != nil {
			return nil, cancelErr
		}
		return nil, errcode.WrapInfra(errcode.ErrConfigRepoQuery, "config repo: rows error", err)
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
		ct, keyID, nonce, edk, encErr := r.encryptVersionValue(ctx, version.ConfigID, version.Value)
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
		if cancelErr := ctxcancel.Wrap(err, "PublishVersion", "configID="+version.ConfigID); cancelErr != nil {
			return cancelErr
		}
		return errcode.WrapInfra(errcode.ErrConfigRepoQuery,
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
		if infraErr := ctxcancel.Wrap(err, "GetVersion", "configID="+configID); infraErr != nil {
			return nil, infraErr
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &errcode.Error{
				Code:            errcode.ErrConfigRepoNotFound,
				Message:         "config version not found",
				InternalMessage: fmt.Sprintf("config repo: GetVersion miss config_id=%s version=%d", configID, version),
				Cause:           err,
				Category:        errcode.CategoryDomain,
			}
		}
		return nil, &errcode.Error{
			Code:            errcode.ErrConfigRepoQuery,
			Message:         "config repo query failed",
			InternalMessage: fmt.Sprintf("config repo: GetVersion scan error config_id=%s version=%d", configID, version),
			Cause:           err,
			Category:        errcode.CategoryInfra,
		}
	}

	// Fail-closed enforcement for sensitive versions.
	if v.Sensitive {
		if len(valueCipher) == 0 || valueKeyID == nil || *valueKeyID == "" {
			// Legacy plaintext version row: block read until plaintext_migration completes.
			return nil, errcode.New(errcode.ErrConfigDecryptFailed,
				"sensitive version is in legacy plaintext format; run plaintext_migration tool before reading")
		}
		plain, err := r.decryptVersionValue(ctx, v.ConfigID, valueCipher, *valueKeyID, valueNonce, valueEDK)
		if err != nil {
			return nil, err
		}
		v.Value = plain
	}

	return &v, nil
}
