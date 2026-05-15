// Package postgres provides a PostgreSQL implementation of configcore ports.
// It does NOT import adapters/postgres — it defines its own DBTX interface
// to match pgx.Tx / pgxpool.Pool, keeping the cell decoupled from the adapter layer.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	configcrypto "github.com/ghbvf/gocell/cells/configcore/internal/crypto"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/state/cas"
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
// status is explicit: context cancellation is handled by ctxcancel, transient
// key-provider faults are unavailable, and all other crypto failures are
// internal.
//
// ref: google/tink aead/subtle/aes_gcm.go — symmetric crypto errors do not
// carry key identifiers in Error() strings.
// ref: FiloSottile/age age.go — encrypt errors use recipient index, not key.
func (r *ConfigRepository) cryptoOpError(code errcode.Code, op, identifier string, cause error) *errcode.Error {
	if cancelErr := ctxcancel.Wrap(cause, op, identifier); cancelErr != nil {
		return cancelErr
	}
	category := errcode.CategoryAuth
	kind := errcode.KindInternal
	if errcode.IsTransient(cause) {
		category = errcode.CategoryInfra
		kind = errcode.KindUnavailable
	}
	return errcode.Wrap(kind, code, "config repo operation failed", cause,
		errcode.WithInternal(fmt.Sprintf("op=%s identifier=%s", op, identifier)),
		errcode.WithCategory(category),
	)
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

const configRepoQueryFailedMessage = "config repo query failed"

// op label constants distinguish doUpdate caller paths in InternalMessage.
// Single-source: doUpdate caller and tests both reference these constants —
// AI-rebust Medium. Hardcode regression in caller would surface via op-label
// assertion in TestConfigRepository_Update{,ForRollback}_NotFound.
const (
	opUpdate            = "Update"
	opUpdateForRollback = "UpdateForRollback"
)

// Diagnostic message + format constants reused across Update / UpdateForRollback /
// Delete / resolveUpdateConflict 404 paths. Centralized per SonarCloud
// design.duplicate-literal: a single-source message keeps client-visible text
// uniform and Internal format consistent for ops log correlation.
const (
	msgConfigNotFound        = "config not found"
	internalFmtConfigMissKey = "config repo: %s miss key=%s"
)

type encryptedPayload struct {
	Ciphertext []byte
	KeyID      string
	Nonce      []byte
	EDK        []byte
}

// Compile-time interface check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

// ConfigRepository implements ports.ConfigRepository using PostgreSQL.
type ConfigRepository struct {
	db          DBTX     // test-only: set by newConfigRepositoryFromDBTX (unexported helper in test file)
	session     *Session // production path: resolves ambient tx via persistence.TxCtxKey
	transformer kcrypto.ValueTransformer
	logger      *slog.Logger
	clock       clock.Clock
	// onStaleCipher is an optional callback invoked when a stale-key value is
	// detected during a read. Callers may wire this to a prometheus counter:
	//   repo.onStaleCipher = func(_, _, _ string) { staleCipherTotal.Inc() }
	// The callback receives (key, storedKeyID, currentKeyID). When nil, it is
	// skipped; slog.Warn is always emitted regardless.
	onStaleCipher func(key, storedKeyID, currentKeyID string)
}

// ConfigRepoOption configures optional behavior on ConfigRepository.
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
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
// If logger is nil, slog.Default() is used.
//
// Requires migrations 001–010 to be applied first (see adapters/postgres/migrations/).
func NewConfigRepository(
	s *Session, tr kcrypto.ValueTransformer, logger *slog.Logger,
	clk clock.Clock, opts ...ConfigRepoOption,
) *ConfigRepository {
	clock.MustHaveClock(clk, "postgres.NewConfigRepository")
	if logger == nil {
		logger = slog.Default()
	}
	r := &ConfigRepository{session: s, transformer: tr, logger: logger, clock: clk}
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
func (r *ConfigRepository) encryptValue(ctx context.Context, key, value string) (encryptedPayload, error) {
	if r.transformer == nil {
		return encryptedPayload{}, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing,
			"config repo: no ValueTransformer configured for sensitive entry")
	}
	aad := configcrypto.AADForConfig(cellID, key)
	result, err := r.transformer.Encrypt(ctx, []byte(value), aad)
	if err != nil {
		return encryptedPayload{}, r.cryptoOpError(errcode.ErrConfigEncryptFailed, "Encrypt", "key="+key, err)
	}
	return encryptedPayload{
		Ciphertext: result.Ciphertext,
		KeyID:      result.KeyID,
		Nonce:      result.Nonce,
		EDK:        result.EDK,
	}, nil
}

// decryptValue decrypts a cipher-column tuple for a sensitive entry.
// Fail-closed: returns ErrConfigDecryptFailed on any error.
func (r *ConfigRepository) decryptValue(ctx context.Context, key string, ct []byte, keyID string, nonce, edk []byte) (string, error) {
	if r.transformer == nil {
		return "", errcode.New(errcode.KindInternal, errcode.ErrConfigDecryptFailed,
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
func (r *ConfigRepository) encryptVersionValue(
	ctx context.Context, configID, value string,
) (encryptedPayload, error) {
	if r.transformer == nil {
		return encryptedPayload{}, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing,
			"config repo: no ValueTransformer configured for sensitive version")
	}
	aad := configcrypto.AADForVersion(cellID, configID)
	result, err := r.transformer.Encrypt(ctx, []byte(value), aad)
	if err != nil {
		return encryptedPayload{}, r.cryptoOpError(errcode.ErrConfigEncryptFailed, "EncryptVersion", "config_id="+configID, err)
	}
	return encryptedPayload{
		Ciphertext: result.Ciphertext,
		KeyID:      result.KeyID,
		Nonce:      result.Nonce,
		EDK:        result.EDK,
	}, nil
}

// decryptVersionValue decrypts a cipher-column tuple for a sensitive config version.
// Uses AADForVersion so the AAD matches the write path in encryptVersionValue.
// Fail-closed: returns ErrConfigDecryptFailed on any error.
func (r *ConfigRepository) decryptVersionValue(
	ctx context.Context, configID string, ct []byte, keyID string, nonce, edk []byte,
) (string, error) {
	if r.transformer == nil {
		return "", errcode.New(errcode.KindInternal, errcode.ErrConfigDecryptFailed,
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
	now := r.clock.Now()
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
		payload, encErr := r.encryptValue(ctx, entry.Key, entry.Value)
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
			payload.Ciphertext, payload.KeyID, payload.EDK, payload.Nonce,
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
		return errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery, "config repo: create failed", err,
			errcode.WithInternal(fmt.Sprintf("config repo: Create failed (key=%s)", entry.Key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	return nil
}

// configEntryColumns is the canonical column list for config_entries used by
// every SELECT/RETURNING projection in this file. Centralized so the column
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
func (r *ConfigRepository) scanConfigOrMapError(
	_ context.Context, row Row, op, key string,
) (*domain.ConfigEntry, []byte, *string, []byte, []byte, error) {
	e, ct, keyID, edk, nonce, err := scanConfigRow(row)
	if err == nil {
		return e, ct, keyID, edk, nonce, nil
	}
	if infraErr := ctxcancel.Wrap(err, op, "key="+key); infraErr != nil {
		return nil, nil, nil, nil, nil, infraErr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil, nil, errcode.Wrap(errcode.KindNotFound, errcode.ErrConfigRepoNotFound,
			msgConfigNotFound, err,
			errcode.WithInternal(fmt.Sprintf(internalFmtConfigMissKey, op, key)),
			errcode.WithCategory(errcode.CategoryDomain),
		)
	}
	return nil, nil, nil, nil, nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
		configRepoQueryFailedMessage, err,
		errcode.WithInternal(fmt.Sprintf("config repo: %s scan error key=%s", op, key)),
		errcode.WithCategory(errcode.CategoryInfra),
	)
}

// decryptScannedEntry applies fail-closed sensitive-value decryption and stale-key
// detection to an already-scanned ConfigEntry. For non-sensitive entries it is a
// no-op. The cipher tuple fields (ct, keyID, edk, nonce) are the raw values
// returned by scanConfigRow.
func (r *ConfigRepository) decryptScannedEntry(
	ctx context.Context, e *domain.ConfigEntry, ct []byte, keyID *string, nonce, edk []byte,
) error {
	if !e.Sensitive {
		return nil
	}
	if len(ct) == 0 || keyID == nil || *keyID == "" {
		// Legacy plaintext row: sensitive=true but value_cipher IS NULL.
		return errcode.New(errcode.KindInternal, errcode.ErrConfigDecryptFailed,
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

// Update atomically sets value and increments version if expectedVersion matches
// the current version (CAS guard). Preserves the existing sensitive flag — reads
// it internally via SELECT...FOR UPDATE to eliminate any TOCTOU race on the
// sensitive flag. Returns ErrConfigRepoNotFound if the key does not exist, or
// ErrVersionConflict if expectedVersion does not match the stored version.
func (r *ConfigRepository) Update(ctx context.Context, key string, expectedVersion int, value string) (*domain.ConfigEntry, error) {
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
			return nil, errcode.Wrap(errcode.KindNotFound, errcode.ErrConfigRepoNotFound,
				msgConfigNotFound, scanErr,
				errcode.WithInternal(fmt.Sprintf(internalFmtConfigMissKey, opUpdate, key)),
				errcode.WithCategory(errcode.CategoryDomain),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
			configRepoQueryFailedMessage, scanErr,
			errcode.WithInternal(fmt.Sprintf("config repo: %s select-for-update error key=%s", opUpdate, key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}

	return r.doUpdate(ctx, db, opUpdate, key, expectedVersion, value, sensitive)
}

// UpdateForRollback atomically sets value AND sensitive, increments version if
// expectedVersion matches the current version (CAS guard).
// Used exclusively by configpublish.Rollback to restore a snapshot's sensitivity
// alongside its value. Returns ErrConfigRepoNotFound if the key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
func (r *ConfigRepository) UpdateForRollback(
	ctx context.Context, key string, expectedVersion int, value string, sensitive bool,
) (*domain.ConfigEntry, error) {
	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	return r.doUpdate(ctx, db, opUpdateForRollback, key, expectedVersion, value, sensitive)
}

// doUpdate performs the actual UPDATE...RETURNING for both Update and
// UpdateForRollback. op identifies the calling method for InternalMessage context.
// expectedVersion is the CAS guard: the UPDATE WHERE clause includes
// AND version=$expectedVersion so that a stale caller gets rowsAffected=0.
// sensitive is the resolved flag (read by caller or provided directly).
// For sensitive=true: re-encrypts value and writes cipher columns.
//
// CAS flow: rowsAffected==0 → probe GetByKey:
//   - exists → ErrVersionConflict (409)
//   - not found → ErrConfigRepoNotFound (404)
func (r *ConfigRepository) doUpdate(
	ctx context.Context, db DBTX, op, key string, expectedVersion int, value string, sensitive bool,
) (*domain.ConfigEntry, error) {
	var (
		rowsAffected int64
		row          Row
	)
	if sensitive {
		payload, encErr := r.encryptValue(ctx, key, value)
		if encErr != nil {
			return nil, encErr
		}
		const q = `UPDATE config_entries
			SET value = '', sensitive = true, version = version+1, updated_at = now(),
			    value_cipher = $1, value_key_id = $2, value_edk = $3, value_nonce = $4
			WHERE key = $5 AND version = $6
			RETURNING ` + configEntryColumns
		row = db.QueryRow(ctx, q, payload.Ciphertext, payload.KeyID, payload.EDK, payload.Nonce, key, expectedVersion)
	} else {
		const q = `UPDATE config_entries
			SET value = $1, sensitive = false, version = version+1, updated_at = now(),
			    value_cipher = NULL, value_key_id = NULL, value_edk = NULL, value_nonce = NULL
			WHERE key = $2 AND version = $3
			RETURNING ` + configEntryColumns
		row = db.QueryRow(ctx, q, value, key, expectedVersion)
	}

	e, ct, keyID, edk, nonce, scanErr := scanConfigRow(row)
	if scanErr != nil {
		if cancelErr := ctxcancel.Wrap(scanErr, op, "key="+key); cancelErr != nil {
			return nil, cancelErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// rowsAffected==0: distinguish version mismatch from not-found.
			return nil, r.resolveUpdateConflict(ctx, op, key)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
			configRepoQueryFailedMessage, scanErr,
			errcode.WithInternal(fmt.Sprintf("config repo: %s scan error key=%s", op, key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	_ = rowsAffected // row scan succeeded → rowsAffected implicitly 1

	if err := r.decryptScannedEntry(ctx, e, ct, keyID, nonce, edk); err != nil {
		return nil, err
	}
	return e, nil
}

// resolveUpdateConflict probes whether a key exists after an UPDATE...WHERE version=$N
// returned no rows. Three-way classification (PR464 P1.2 fix):
//
//   - Probe returns ErrConfigRepoNotFound → key absent → 404
//   - Probe returns any other error (timeout, tx aborted, scan failure) → infra
//     fault — transparent passthrough as internal (do NOT masquerade as 404)
//   - Probe succeeds → key exists but version mismatch → 409 ErrVersionConflict
//
// ref: docs/reviews/PR-464 round-2 P1.2 (Kratos/Watermill/etcd: probe failure
// must not collapse into business not-found).
func (r *ConfigRepository) resolveUpdateConflict(ctx context.Context, op, key string) error {
	probe, probeErr := r.GetByKey(ctx, key)
	if probeErr != nil {
		notFound, infraErr := classifyProbeFailure(probeErr, errcode.ErrConfigRepoNotFound, op, key, "config_entry")
		if !notFound {
			return infraErr
		}
		// Confirmed key absent → 404.
		return errcode.Wrap(errcode.KindNotFound, errcode.ErrConfigRepoNotFound,
			msgConfigNotFound, probeErr,
			errcode.WithInternal(fmt.Sprintf(internalFmtConfigMissKey, op, key)),
			errcode.WithCategory(errcode.CategoryDomain),
		)
	}
	// Key exists but version did not match → 409.
	return cas.CheckVersionMatch(0, "config_entry", probe.Key)
}

// Delete atomically removes a config entry by key if expectedVersion matches the
// stored version (CAS guard). Returns the deleted row via DELETE...RETURNING,
// enabling callers to publish a tombstone event without a separate pre-read.
// Returns ErrConfigRepoNotFound if the key does not exist, or ErrVersionConflict
// if expectedVersion does not match.
func (r *ConfigRepository) Delete(ctx context.Context, key string, expectedVersion int) (*domain.ConfigEntry, error) {
	const q = `DELETE FROM config_entries WHERE key = $1 AND version = $2 RETURNING ` + configEntryColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(ctx, q, key, expectedVersion)
	e, ct, keyID, edk, nonce, scanErr := scanConfigRow(row)
	if scanErr != nil {
		if cancelErr := ctxcancel.Wrap(scanErr, "Delete", "key="+key); cancelErr != nil {
			return nil, cancelErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, r.resolveUpdateConflict(ctx, "Delete", key)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
			configRepoQueryFailedMessage, scanErr,
			errcode.WithInternal(fmt.Sprintf("config repo: Delete scan error key=%s", key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
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
func (r *ConfigRepository) applySensitiveListSentinel(
	ctx context.Context, e *domain.ConfigEntry, valueKeyID *string,
	currentID string, staleLogged map[string]bool,
) {
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
	b := pgquery.NewBuilder()
	b.Append("SELECT " + listEntryColumns + " FROM config_entries WHERE 1=1")

	if err := pgquery.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery, "config repo: keyset build failed", err)
	}

	sql, args := b.Build()
	rows, err := r.resolveDB(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "List", "",
			errcode.ErrConfigRepoQuery, "config repo: list failed")
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
			return nil, ctxcancel.WrapOrInfra(err, "List", "",
				errcode.ErrConfigRepoQuery, "config repo: scan failed")
		}
		if e.Sensitive {
			r.applySensitiveListSentinel(ctx, &e, valueKeyID, currentID, staleLogged)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "List", "",
			errcode.ErrConfigRepoQuery, "config repo: rows error")
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
		payload, encErr := r.encryptVersionValue(ctx, version.ConfigID, version.Value)
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
			payload.Ciphertext, payload.KeyID, payload.EDK, payload.Nonce,
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
		return errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery, "config repo: publish version failed", err,
			errcode.WithInternal(fmt.Sprintf("configID=%s version=%d", version.ConfigID, version.Version)),
			errcode.WithCategory(errcode.CategoryInfra))
	}
	return nil
}

// configEntriesProbeSQL is a representative query for the config_entries table
// that returns no rows (WHERE false short-circuits the scan) but fails if the
// table is missing, dropped, or has revoked table-level permissions.
const configEntriesProbeSQL = `SELECT 1 FROM config_entries WHERE false`

// featureFlagsProbeSQL is the equivalent representative query for the
// feature_flags table, providing a differentiated failure domain from
// config_entries for the RepoReady probe.
const featureFlagsProbeSQL = `SELECT 1 FROM feature_flags WHERE false`

// RepoReady implements cell.RepoHealthProber. It issues two cheap
// non-transactional representative queries — SELECT 1 FROM config_entries WHERE
// false and SELECT 1 FROM feature_flags WHERE false — so that missing tables,
// dropped columns, or revoked table-level permissions are detected independently
// of the pool-level postgres_ready probe. Neither query returns rows (WHERE
// false short-circuits the scan), so there is no latency overhead from result
// iteration. No transaction is opened.
func (r *ConfigRepository) RepoReady(ctx context.Context) error {
	db := r.resolveDB(ctx)

	rows, err := db.Query(ctx, configEntriesProbeSQL)
	if err != nil {
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrConfigRepoQuery,
			"config repo readiness check failed", err,
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrConfigRepoQuery,
			"config repo readiness check failed", err,
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}

	rows, err = db.Query(ctx, featureFlagsProbeSQL)
	if err != nil {
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrConfigRepoQuery,
			"config repo readiness check failed", err,
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrConfigRepoQuery,
			"config repo readiness check failed", err,
			errcode.WithCategory(errcode.CategoryInfra),
		)
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
			return nil, errcode.Wrap(errcode.KindNotFound, errcode.ErrConfigRepoNotFound,
				"config version not found", err,
				errcode.WithInternal(fmt.Sprintf("config repo: GetVersion miss config_id=%s version=%d", configID, version)),
				errcode.WithCategory(errcode.CategoryDomain),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
			configRepoQueryFailedMessage, err,
			errcode.WithInternal(fmt.Sprintf("config repo: GetVersion scan error config_id=%s version=%d", configID, version)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}

	// Fail-closed enforcement for sensitive versions.
	if v.Sensitive {
		if len(valueCipher) == 0 || valueKeyID == nil || *valueKeyID == "" {
			// Legacy plaintext version row: block read until plaintext_migration completes.
			return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigDecryptFailed,
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
