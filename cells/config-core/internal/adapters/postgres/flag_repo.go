// Package postgres provides a PostgreSQL implementation of config-core ports.
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

// Compile-time interface check.
var _ ports.FlagRepository = (*FlagRepository)(nil)

// flagColumns is the canonical column list for feature_flags used by every
// SELECT/RETURNING projection in this file. Centralised so the column order
// stays in sync between the INSERT, SELECT, RETURNING, and scanFlagRow calls.
const flagColumns = "id, key, enabled, rollout_percentage, description, version, created_at, updated_at"

// FlagRepository implements ports.FlagRepository using PostgreSQL.
//
// Write paths (Create/Update/Delete/Toggle) require an ambient pgx.Tx in ctx
// via persistence.TxCtxKey — enforced by resolveWriteDB. Read paths (GetByKey/List)
// fall back to the pool when no tx is present.
//
// ref: Unleash feature-environment-store.ts — Toggle is a dedicated method
// that does NOT overwrite rollout_percentage or description, preventing
// concurrent-write data loss (Unleash lesson: "write + event must be atomic").
type FlagRepository struct {
	db      DBTX     // test-only: set by newFlagRepositoryFromDBTX
	session *Session // production path: resolves ambient tx via persistence.TxCtxKey
}

// NewFlagRepository creates a FlagRepository that resolves the ambient
// pgx.Tx from the context on each write call.
//
// Requires migrations 008 (table) and 009 (concurrent index) to be applied.
// The adapterpg schema guard (VerifyExpectedVersion) enforces the actual
// current expected version at startup; this comment is documentation-only
// and deliberately does not duplicate that check.
func NewFlagRepository(s *Session) *FlagRepository {
	return &FlagRepository{session: s}
}

func (r *FlagRepository) resolveDB(ctx context.Context) DBTX {
	if r.session != nil {
		return r.session.resolve(ctx)
	}
	return r.db
}

func (r *FlagRepository) resolveWriteDB(ctx context.Context) (DBTX, error) {
	if r.session != nil {
		return r.session.resolveWrite(ctx)
	}
	return r.db, nil
}

// scanFlagRow scans a single row (order matches flagColumns) into a
// domain.FeatureFlag. Called by GetByKey/Update/Delete/Toggle so the field
// order stays in sync with the SELECT/RETURNING projections.
func scanFlagRow(row pgx.Row) (*domain.FeatureFlag, error) {
	var f domain.FeatureFlag
	if err := row.Scan(
		&f.ID, &f.Key, &f.Enabled, &f.RolloutPercentage,
		&f.Description, &f.Version, &f.CreatedAt, &f.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &f, nil
}

// scanFlagOrMapError runs scanFlagRow and translates the two known failure
// modes into GoCell errcode.Error values:
//
//   - pgx.ErrNoRows  → ErrFlagNotFound (domain not-found; maps to 404).
//   - anything else  → ErrFlagRepoQuery (infra failure; maps to 500).
//
// op is the method name (e.g. "Update") used only in InternalMessage for
// operator-side debugging; key is the lookup key surfaced the same way.
// Centralising this mapping removes the ~40 lines of repeated error
// boilerplate previously scattered across Update/Delete/Toggle/GetByKey.
func scanFlagOrMapError(row pgx.Row, op, key string) (*domain.FeatureFlag, error) {
	flag, err := scanFlagRow(row)
	if err == nil {
		return flag, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &errcode.Error{
			Code:            errcode.ErrFlagNotFound,
			Message:         "flag not found",
			InternalMessage: fmt.Sprintf("flag repo: %s miss key=%s", op, key),
			Cause:           err,
		}
	}
	return nil, &errcode.Error{
		Code:            errcode.ErrFlagRepoQuery,
		Message:         "flag repo query failed",
		InternalMessage: fmt.Sprintf("flag repo: %s scan error key=%s", op, key),
		Cause:           err,
	}
}

// Create inserts a new feature flag. All 8 columns are written.
func (r *FlagRepository) Create(ctx context.Context, flag *domain.FeatureFlag) error {
	const sql = `INSERT INTO feature_flags
		(` + flagColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	now := time.Now()
	if flag.CreatedAt.IsZero() {
		flag.CreatedAt = now
	}
	if flag.UpdatedAt.IsZero() {
		flag.UpdatedAt = now
	}
	if flag.Version == 0 {
		flag.Version = 1
	}

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, sql,
		flag.ID, flag.Key, flag.Enabled, flag.RolloutPercentage,
		flag.Description, flag.Version, flag.CreatedAt, flag.UpdatedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrFlagRepoQuery,
			fmt.Sprintf("flag repo: create failed for key %s", flag.Key), err)
	}
	return nil
}

// GetByKey retrieves a feature flag by key.
func (r *FlagRepository) GetByKey(ctx context.Context, key string) (*domain.FeatureFlag, error) {
	const sql = `SELECT ` + flagColumns + ` FROM feature_flags WHERE key = $1`
	return scanFlagOrMapError(r.resolveDB(ctx).QueryRow(ctx, sql, key), "GetByKey", key)
}

// Update atomically sets enabled, rollout_percentage, description, and
// increments version by 1 via UPDATE...SET version=version+1 RETURNING.
// Returns the updated flag. Returns ErrFlagNotFound if key does not exist.
func (r *FlagRepository) Update(ctx context.Context, key string, enabled bool, rolloutPercentage int, description string) (*domain.FeatureFlag, error) {
	const sql = `UPDATE feature_flags
		SET enabled=$1, rollout_percentage=$2, description=$3, version=version+1, updated_at=now()
		WHERE key=$4
		RETURNING ` + flagColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	return scanFlagOrMapError(db.QueryRow(ctx, sql, enabled, rolloutPercentage, description, key), "Update", key)
}

// List retrieves feature flags with keyset cursor pagination.
// Requires composite index: CREATE INDEX idx_feature_flags_key_id ON feature_flags (key ASC, id ASC)
func (r *FlagRepository) List(ctx context.Context, params query.ListParams) ([]*domain.FeatureFlag, error) {
	b := query.NewBuilder()
	b.Append("SELECT " + flagColumns + " FROM feature_flags WHERE 1=1")

	if err := query.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.ErrFlagRepoQuery, "flag repo: keyset build failed", err)
	}

	sqlStr, args := b.Build()
	rows, err := r.resolveDB(ctx).Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrFlagRepoQuery, "flag repo: list failed", err)
	}
	defer rows.Close()

	var flags []*domain.FeatureFlag
	for rows.Next() {
		flag, scanErr := scanFlagRow(rows)
		if scanErr != nil {
			return nil, errcode.Wrap(errcode.ErrFlagRepoQuery, "flag repo: scan failed", scanErr)
		}
		flags = append(flags, flag)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrFlagRepoQuery, "flag repo: rows error", err)
	}
	return flags, nil
}

// Delete removes a feature flag by key and returns the deleted entity via
// DELETE...RETURNING. Returns ErrFlagNotFound if the key does not exist.
func (r *FlagRepository) Delete(ctx context.Context, key string) (*domain.FeatureFlag, error) {
	const sql = `DELETE FROM feature_flags WHERE key=$1 RETURNING ` + flagColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	return scanFlagOrMapError(db.QueryRow(ctx, sql, key), "Delete", key)
}

// Toggle atomically sets the enabled state and increments version by 1.
// It does NOT overwrite rollout_percentage or description.
// Returns the updated flag via RETURNING clause.
//
// ref: Unleash feature-environment-store.ts toggleEnvironment — dedicated toggle
// method prevents concurrent overwrites on unrelated fields.
func (r *FlagRepository) Toggle(ctx context.Context, key string, enabled bool) (*domain.FeatureFlag, error) {
	const sql = `UPDATE feature_flags
		SET enabled=$1, version=version+1, updated_at=now()
		WHERE key=$2
		RETURNING ` + flagColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	return scanFlagOrMapError(db.QueryRow(ctx, sql, enabled, key), "Toggle", key)
}
