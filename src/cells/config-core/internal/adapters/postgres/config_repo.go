// Package postgres provides a PostgreSQL implementation of config-core ports.
// It does NOT import adapters/postgres — it defines its own DBTX interface
// to match pgx.Tx / pgxpool.Pool, keeping the cell decoupled from the adapter layer.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	// ErrConfigRepoQuery indicates a query execution failure.
	ErrConfigRepoQuery errcode.Code = "ERR_CONFIG_REPO_QUERY"
	// ErrConfigRepoNotFound indicates a record was not found.
	ErrConfigRepoNotFound errcode.Code = "ERR_CONFIG_REPO_NOT_FOUND"
	// ErrConfigRepoDuplicate indicates a duplicate key.
	ErrConfigRepoDuplicate errcode.Code = "ERR_CONFIG_REPO_DUPLICATE"

	// listLimit is the safety-net row limit for unbounded queries.
	listLimit = 1000
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
	db DBTX
}

// NewConfigRepository creates a ConfigRepository backed by the given DBTX.
func NewConfigRepository(db DBTX) *ConfigRepository {
	return &ConfigRepository{db: db}
}

// Create inserts a new config entry.
func (r *ConfigRepository) Create(ctx context.Context, entry *domain.ConfigEntry) error {
	const query = `INSERT INTO config_entries
		(id, key, value, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = now
	}

	_, err := r.db.Exec(ctx, query,
		entry.ID, entry.Key, entry.Value, entry.Version,
		entry.CreatedAt, entry.UpdatedAt,
	)
	if err != nil {
		return errcode.Wrap(ErrConfigRepoQuery,
			fmt.Sprintf("config repo: create failed for key %s", entry.Key), err)
	}

	return nil
}

// GetByKey retrieves a config entry by key.
func (r *ConfigRepository) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	const query = `SELECT id, key, value, version, created_at, updated_at
		FROM config_entries WHERE key = $1`

	row := r.db.QueryRow(ctx, query, key)

	var e domain.ConfigEntry
	if err := row.Scan(&e.ID, &e.Key, &e.Value, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, errcode.Wrap(ErrConfigRepoNotFound,
			fmt.Sprintf("config repo: key not found: %s", key), err)
	}

	return &e, nil
}

// Update updates an existing config entry.
func (r *ConfigRepository) Update(ctx context.Context, entry *domain.ConfigEntry) error {
	const query = `UPDATE config_entries
		SET value = $1, version = $2, updated_at = $3
		WHERE key = $4`

	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}

	affected, err := r.db.Exec(ctx, query,
		entry.Value, entry.Version, entry.UpdatedAt, entry.Key,
	)
	if err != nil {
		return errcode.Wrap(ErrConfigRepoQuery,
			fmt.Sprintf("config repo: update failed for key %s", entry.Key), err)
	}
	if affected == 0 {
		return errcode.New(ErrConfigRepoNotFound,
			fmt.Sprintf("config repo: key not found: %s", entry.Key))
	}

	return nil
}

// Delete removes a config entry by key.
func (r *ConfigRepository) Delete(ctx context.Context, key string) error {
	const query = `DELETE FROM config_entries WHERE key = $1`

	affected, err := r.db.Exec(ctx, query, key)
	if err != nil {
		return errcode.Wrap(ErrConfigRepoQuery,
			fmt.Sprintf("config repo: delete failed for key %s", key), err)
	}
	if affected == 0 {
		return errcode.New(ErrConfigRepoNotFound,
			fmt.Sprintf("config repo: key not found: %s", key))
	}

	return nil
}

// List retrieves all config entries with a safety-net LIMIT.
func (r *ConfigRepository) List(ctx context.Context) ([]*domain.ConfigEntry, error) {
	const query = `SELECT id, key, value, version, created_at, updated_at
		FROM config_entries ORDER BY key LIMIT 1000`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, errcode.Wrap(ErrConfigRepoQuery, "config repo: list failed", err)
	}
	defer rows.Close()

	var entries []*domain.ConfigEntry
	for rows.Next() {
		var e domain.ConfigEntry
		if err := rows.Scan(&e.ID, &e.Key, &e.Value, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, errcode.Wrap(ErrConfigRepoQuery, "config repo: scan failed", err)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(ErrConfigRepoQuery, "config repo: rows error", err)
	}

	return entries, nil
}

// PublishVersion inserts a config version record.
func (r *ConfigRepository) PublishVersion(ctx context.Context, version *domain.ConfigVersion) error {
	const query = `INSERT INTO config_versions
		(id, config_id, version, value, published_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.db.Exec(ctx, query,
		version.ID, version.ConfigID, version.Version,
		version.Value, version.PublishedAt,
	)
	if err != nil {
		return errcode.Wrap(ErrConfigRepoQuery,
			fmt.Sprintf("config repo: publish version failed for config %s v%d",
				version.ConfigID, version.Version), err)
	}

	return nil
}

// GetVersion retrieves a specific config version.
func (r *ConfigRepository) GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error) {
	const query = `SELECT id, config_id, version, value, published_at
		FROM config_versions WHERE config_id = $1 AND version = $2`

	row := r.db.QueryRow(ctx, query, configID, version)

	var v domain.ConfigVersion
	if err := row.Scan(&v.ID, &v.ConfigID, &v.Version, &v.Value, &v.PublishedAt); err != nil {
		return nil, errcode.Wrap(ErrConfigRepoNotFound,
			fmt.Sprintf("config repo: version not found: %s v%d", configID, version), err)
	}

	return &v, nil
}
