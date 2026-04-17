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
	db      DBTX    // set when constructed directly with a fixed DBTX (test path)
	session *Session // set when constructed from a Session (production path)
}

// NewConfigRepository creates a ConfigRepository backed by the given DBTX.
// Prefer NewConfigRepositoryFromSession for production use.
func NewConfigRepository(db DBTX) *ConfigRepository {
	return &ConfigRepository{db: db}
}

// NewConfigRepositoryFromSession creates a ConfigRepository that resolves the
// ambient pgx.Tx from the context on each call, enabling transactional
// participation via persistence.TxCtxKey.
func NewConfigRepositoryFromSession(s *Session) *ConfigRepository {
	return &ConfigRepository{session: s}
}

// resolveDB returns the DBTX to use for this call. When a Session is
// configured it resolves the ambient transaction from ctx; otherwise the
// fixed DBTX is used (unit-test path).
func (r *ConfigRepository) resolveDB(ctx context.Context) DBTX {
	if r.session != nil {
		return r.session.resolve(ctx)
	}
	return r.db
}

// Create inserts a new config entry.
func (r *ConfigRepository) Create(ctx context.Context, entry *domain.ConfigEntry) error {
	const query = `INSERT INTO config_entries
		(id, key, value, sensitive, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = now
	}

	_, err := r.resolveDB(ctx).Exec(ctx, query,
		entry.ID, entry.Key, entry.Value, entry.Sensitive, entry.Version,
		entry.CreatedAt, entry.UpdatedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("config repo: create failed for key %s", entry.Key), err)
	}

	return nil
}

// GetByKey retrieves a config entry by key.
func (r *ConfigRepository) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	const query = `SELECT id, key, value, sensitive, version, created_at, updated_at
		FROM config_entries WHERE key = $1`

	row := r.resolveDB(ctx).QueryRow(ctx, query, key)

	var e domain.ConfigEntry
	if err := row.Scan(&e.ID, &e.Key, &e.Value, &e.Sensitive, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
		// REPO-SCAN-CLASSIFY-01: distinguish pgx.ErrNoRows (404) from other
		// scan errors (internal/query error). Previously all scan errors were
		// mapped to ErrConfigRepoNotFound which hid real DB failures.
		// ref: go-zero sqlx — sql.ErrNoRows as sentinel for not-found.
		if errors.Is(err, pgx.ErrNoRows) {
			// PR#155 followup F3: Message is the externally visible string for 4xx
			// (writeErrcodeError pass-through). Keep it identifier-free; the key
			// goes into InternalMessage which is logged but never written to the
			// HTTP response. ref: pkg/errcode.Safe.
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

	return &e, nil
}

// Update updates an existing config entry.
func (r *ConfigRepository) Update(ctx context.Context, entry *domain.ConfigEntry) error {
	const query = `UPDATE config_entries
		SET value = $1, sensitive = $2, version = $3, updated_at = $4
		WHERE key = $5`

	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}

	affected, err := r.resolveDB(ctx).Exec(ctx, query,
		entry.Value, entry.Sensitive, entry.Version, entry.UpdatedAt, entry.Key,
	)
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
	const query = `DELETE FROM config_entries WHERE key = $1`

	affected, err := r.resolveDB(ctx).Exec(ctx, query, key)
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

// List retrieves config entries with keyset cursor pagination.
// Requires composite index: CREATE INDEX idx_config_entries_key_id ON config_entries (key ASC, id ASC)
func (r *ConfigRepository) List(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
	b := query.NewBuilder()
	b.Append("SELECT id, key, value, sensitive, version, created_at, updated_at FROM config_entries WHERE 1=1")

	if err := query.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: keyset build failed", err)
	}

	sql, args := b.Build()
	rows, err := r.resolveDB(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: list failed", err)
	}
	defer rows.Close()

	var entries []*domain.ConfigEntry
	for rows.Next() {
		var e domain.ConfigEntry
		if err := rows.Scan(&e.ID, &e.Key, &e.Value, &e.Sensitive, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: scan failed", err)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery, "config repo: rows error", err)
	}

	return entries, nil
}

// PublishVersion inserts a config version record.
func (r *ConfigRepository) PublishVersion(ctx context.Context, version *domain.ConfigVersion) error {
	const query = `INSERT INTO config_versions
		(id, config_id, version, value, sensitive, published_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	_, err := r.resolveDB(ctx).Exec(ctx, query,
		version.ID, version.ConfigID, version.Version,
		version.Value, version.Sensitive, version.PublishedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("config repo: publish version failed for config %s v%d",
				version.ConfigID, version.Version), err)
	}

	return nil
}

// GetVersion retrieves a specific config version.
func (r *ConfigRepository) GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error) {
	const query = `SELECT id, config_id, version, value, sensitive, published_at
		FROM config_versions WHERE config_id = $1 AND version = $2`

	row := r.resolveDB(ctx).QueryRow(ctx, query, configID, version)

	var v domain.ConfigVersion
	if err := row.Scan(&v.ID, &v.ConfigID, &v.Version, &v.Value, &v.Sensitive, &v.PublishedAt); err != nil {
		// REPO-SCAN-CLASSIFY-01: distinguish pgx.ErrNoRows (404) from other
		// scan errors (internal/query error). Previously all scan errors were
		// mapped to ErrConfigRepoNotFound which hid real DB failures.
		// ref: go-zero sqlx — sql.ErrNoRows as sentinel for not-found.
		if errors.Is(err, pgx.ErrNoRows) {
			// PR#155 followup F3: external Message must not leak the internal config_id
			// or the requested version (would help an attacker enumerate). Identifiers
			// stay in InternalMessage + Cause for logs/diagnostics only.
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

	return &v, nil
}
