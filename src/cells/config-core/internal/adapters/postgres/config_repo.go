// Package postgres implements the config-core ConfigRepository backed by
// PostgreSQL via pgx.
package postgres

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Error codes for config repository operations.
const (
	ErrConfigNotFound  errcode.Code = "ERR_CONFIG_NOT_FOUND"
	ErrConfigDuplicate errcode.Code = "ERR_CONFIG_DUPLICATE"
)

// queryLimit is a safety-net maximum for unbounded queries (ARCH-07).
const queryLimit = 1000

// Compile-time interface check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

// DBTX abstracts pgxpool.Pool and pgx.Tx so the repository can participate
// in transactions.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ConfigRepository is a PostgreSQL-backed implementation of
// ports.ConfigRepository.
type ConfigRepository struct {
	pool *pgxpool.Pool
}

// NewConfigRepository creates a ConfigRepository that uses the provided pgx
// connection pool.
func NewConfigRepository(pool *pgxpool.Pool) *ConfigRepository {
	return &ConfigRepository{pool: pool}
}

// db returns either the transaction from context or the pool.
func (r *ConfigRepository) db(ctx context.Context) DBTX {
	if tx := TxFromContext(ctx); tx != nil {
		return tx
	}
	return r.pool
}

// Create inserts a new config entry. Returns ErrConfigDuplicate if the key
// already exists.
func (r *ConfigRepository) Create(ctx context.Context, entry *domain.ConfigEntry) error {
	const q = `INSERT INTO config_entries (id, key, value, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (key) DO NOTHING`

	tag, err := r.db(ctx).Exec(ctx, q,
		entry.ID, entry.Key, entry.Value, entry.Version,
		entry.CreatedAt, entry.UpdatedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, fmt.Sprintf("config_repo: create %s", entry.Key), err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(ErrConfigDuplicate, "config key already exists: "+entry.Key)
	}
	return nil
}

// GetByKey retrieves a config entry by its unique key. Returns
// ErrConfigNotFound if the key does not exist.
func (r *ConfigRepository) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	const q = `SELECT id, key, value, version, created_at, updated_at
		FROM config_entries WHERE key = $1`

	var e domain.ConfigEntry
	err := r.db(ctx).QueryRow(ctx, q, key).Scan(
		&e.ID, &e.Key, &e.Value, &e.Version,
		&e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errcode.New(ErrConfigNotFound, "config not found: "+key)
		}
		return nil, errcode.Wrap(errcode.ErrInternal, fmt.Sprintf("config_repo: get by key %s", key), err)
	}
	return &e, nil
}

// Update modifies an existing config entry identified by key. Returns
// ErrConfigNotFound if the key does not exist.
func (r *ConfigRepository) Update(ctx context.Context, entry *domain.ConfigEntry) error {
	const q = `UPDATE config_entries
		SET value = $1, version = $2, updated_at = $3
		WHERE key = $4`

	tag, err := r.db(ctx).Exec(ctx, q,
		entry.Value, entry.Version, entry.UpdatedAt, entry.Key,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, fmt.Sprintf("config_repo: update %s", entry.Key), err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(ErrConfigNotFound, "config not found: "+entry.Key)
	}
	return nil
}

// Delete removes a config entry by key. Returns ErrConfigNotFound if the key
// does not exist.
func (r *ConfigRepository) Delete(ctx context.Context, key string) error {
	const q = `DELETE FROM config_entries WHERE key = $1`

	tag, err := r.db(ctx).Exec(ctx, q, key)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, fmt.Sprintf("config_repo: delete %s", key), err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(ErrConfigNotFound, "config not found: "+key)
	}
	return nil
}

// List returns all config entries, capped at queryLimit rows (ARCH-07 safety
// net).
func (r *ConfigRepository) List(ctx context.Context) ([]*domain.ConfigEntry, error) {
	const q = `SELECT id, key, value, version, created_at, updated_at
		FROM config_entries
		ORDER BY key ASC
		LIMIT $1`

	rows, err := r.db(ctx).Query(ctx, q, queryLimit)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "config_repo: list", err)
	}
	defer rows.Close()

	var result []*domain.ConfigEntry
	for rows.Next() {
		var e domain.ConfigEntry
		if err := rows.Scan(
			&e.ID, &e.Key, &e.Value, &e.Version,
			&e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, errcode.Wrap(errcode.ErrInternal, "config_repo: scan row", err)
		}
		result = append(result, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "config_repo: rows iteration", err)
	}
	return result, nil
}

// PublishVersion inserts a published version snapshot.
func (r *ConfigRepository) PublishVersion(ctx context.Context, version *domain.ConfigVersion) error {
	const q = `INSERT INTO config_versions (id, config_id, version, value, published_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.db(ctx).Exec(ctx, q,
		version.ID, version.ConfigID, version.Version,
		version.Value, version.PublishedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal,
			fmt.Sprintf("config_repo: publish version %s v%d", version.ConfigID, version.Version), err)
	}
	return nil
}

// GetVersion retrieves a specific published version of a config entry.
// Returns ErrConfigNotFound if the version does not exist.
func (r *ConfigRepository) GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error) {
	const q = `SELECT id, config_id, version, value, published_at
		FROM config_versions
		WHERE config_id = $1 AND version = $2`

	var v domain.ConfigVersion
	err := r.db(ctx).QueryRow(ctx, q, configID, version).Scan(
		&v.ID, &v.ConfigID, &v.Version, &v.Value, &v.PublishedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errcode.New(ErrConfigNotFound, "version not found")
		}
		return nil, errcode.Wrap(errcode.ErrInternal,
			fmt.Sprintf("config_repo: get version %s v%d", configID, version), err)
	}
	return &v, nil
}
