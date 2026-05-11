// Package postgres provides a PostgreSQL implementation of configcore ports.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// Compile-time interface check.
var _ ports.FlagRepository = (*FlagRepository)(nil)

// flagColumns is the canonical column list for feature_flags used by every
// SELECT/RETURNING projection in this file. Centralized so the column order
// stays in sync between the INSERT, SELECT, RETURNING, and scanFlagRow calls.
const flagColumns = "id, key, enabled, rollout_percentage, description, version, created_at, updated_at"

// FlagRepository implements ports.FlagRepository using PostgreSQL.
//
// Write paths (Create/Update/Delete/Toggle) require an ambient pgx.Tx in ctx
// via persistence.TxCtxKey — enforced by resolveWriteDB. Read paths
// (GetByKey/List) fall back to the pool when no tx is present.
//
// ref: Unleash feature-environment-store.ts — Toggle is a dedicated method
// that does NOT overwrite rollout_percentage or description, preventing
// concurrent-write data loss (Unleash lesson: "write + event must be atomic").
type FlagRepository struct {
	db      DBTX     // test-only: set by newFlagRepositoryFromDBTX
	session *Session // production path: resolves ambient tx via persistence.TxCtxKey
	clock   clock.Clock
}

// NewFlagRepository creates a FlagRepository that resolves the ambient
// pgx.Tx from the context on each write call.
//
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
// Requires migrations 008 (table) and 009 (concurrent index) to be applied.
// The adapterpg schema guard (VerifyExpectedVersion) enforces the actual
// current expected version at startup; this comment is documentation-only
// and deliberately does not duplicate that check.
func NewFlagRepository(s *Session, clk clock.Clock) *FlagRepository {
	clock.MustHaveClock(clk, "postgres.NewFlagRepository")
	return &FlagRepository{session: s, clock: clk}
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

// scanFlagOrMapError runs scanFlagRow and translates the three known failure
// modes into GoCell errcode.Error values:
//
//   - context.Canceled / context.DeadlineExceeded → ErrClientCanceled (HTTP 499 + slog.Warn).
//   - pgx.ErrNoRows  → ErrFlagNotFound (domain not-found; maps to 404).
//   - anything else  → ErrFlagRepoQuery (infra failure; maps to 500).
//
// The ctx-cancel guard is checked first to prevent client cancellation from
// being misclassified as a domain not-found condition (mirroring
// scanConfigOrMapError's S15 fix).
//
// op is the method name (e.g. "Update") used only in InternalMessage for
// operator-side debugging; key is the lookup key surfaced the same way.
func (r *FlagRepository) scanFlagOrMapError(_ context.Context, row Row, op, key string) (*domain.FeatureFlag, error) {
	flag, err := scanFlagRow(row)
	if err == nil {
		return flag, nil
	}
	if cancelErr := ctxcancel.Wrap(err, op, "key="+key); cancelErr != nil {
		return nil, cancelErr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.Wrap(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found", err,
			errcode.WithInternal(fmt.Sprintf("flag repo: %s miss key=%s", op, key)),
			errcode.WithCategory(errcode.CategoryDomain),
		)
	}
	return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrFlagRepoQuery, "flag repo query failed", err,
		errcode.WithInternal(fmt.Sprintf("flag repo: %s scan error key=%s", op, key)),
		errcode.WithCategory(errcode.CategoryInfra),
	)
}

// scanFlagRow scans a single row (order matches flagColumns) into a
// domain.FeatureFlag. Called by GetByKey/Update/Delete/Toggle so the field
// order stays in sync with the SELECT/RETURNING projections. Accepts the
// local Row interface (matches both DBTX.QueryRow output and Rows during
// iteration), keeping pgx as an internal adapter detail.
func scanFlagRow(row Row) (*domain.FeatureFlag, error) {
	var f domain.FeatureFlag
	if err := row.Scan(
		&f.ID, &f.Key, &f.Enabled, &f.RolloutPercentage,
		&f.Description, &f.Version, &f.CreatedAt, &f.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &f, nil
}

// Create inserts a new feature flag. All 8 columns are written.
func (r *FlagRepository) Create(ctx context.Context, flag *domain.FeatureFlag) error {
	const sql = `INSERT INTO feature_flags
		(` + flagColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	now := r.clock.Now()
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
	if _, err = db.Exec(ctx, sql,
		flag.ID, flag.Key, flag.Enabled, flag.RolloutPercentage,
		flag.Description, flag.Version, flag.CreatedAt, flag.UpdatedAt,
	); err != nil {
		if cancelErr := ctxcancel.Wrap(err, "Create", "key="+flag.Key); cancelErr != nil {
			return cancelErr
		}
		// InternalMessage carries the key for operator triage; the public
		// Message stays generic so user input never echoes in API responses.
		return errcode.Wrap(errcode.KindInternal, errcode.ErrFlagRepoQuery, "flag repo: create failed", err,
			errcode.WithInternal("flag repo: Create failed (key="+flag.Key+")"),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	return nil
}

// GetByKey retrieves a feature flag by key.
func (r *FlagRepository) GetByKey(ctx context.Context, key string) (*domain.FeatureFlag, error) {
	const sql = `SELECT ` + flagColumns + ` FROM feature_flags WHERE key = $1`
	return r.scanFlagOrMapError(ctx, r.resolveDB(ctx).QueryRow(ctx, sql, key), "GetByKey", key)
}

// Update atomically sets enabled, rollout_percentage, description, and
// increments version by 1 via UPDATE...SET version=version+1 RETURNING.
// Returns the updated flag. Returns ErrFlagNotFound if key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
//
// CAS flow: rowsAffected==0 → probe GetByKey:
//   - exists → ErrVersionConflict (409)
//   - not found → ErrFlagNotFound (404)
func (r *FlagRepository) Update(
	ctx context.Context, key string, expectedVersion int, enabled bool, rolloutPercentage int, description string,
) (*domain.FeatureFlag, error) {
	const sql = `UPDATE feature_flags
		SET enabled=$1, rollout_percentage=$2, description=$3, version=version+1, updated_at=now()
		WHERE key=$4 AND version=$5
		RETURNING ` + flagColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	flag, scanErr := scanFlagRow(db.QueryRow(ctx, sql, enabled, rolloutPercentage, description, key, expectedVersion))
	if scanErr != nil {
		if cancelErr := ctxcancel.Wrap(scanErr, "Update", "key="+key); cancelErr != nil {
			return nil, cancelErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, r.resolveUpdateConflict(ctx, "Update", key)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrFlagRepoQuery, "flag repo query failed", scanErr,
			errcode.WithInternal(fmt.Sprintf("flag repo: Update scan error key=%s", key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	return flag, nil
}

// List retrieves feature flags with keyset cursor pagination.
// Requires composite index: CREATE INDEX idx_feature_flags_key_id ON feature_flags (key ASC, id ASC).
func (r *FlagRepository) List(ctx context.Context, params query.ListParams) ([]*domain.FeatureFlag, error) {
	b := pgquery.NewBuilder()
	b.Append("SELECT " + flagColumns + " FROM feature_flags WHERE 1=1")

	if err := pgquery.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrFlagRepoQuery, "flag repo: keyset build failed", err)
	}

	sqlStr, args := b.Build()
	rows, err := r.resolveDB(ctx).Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "List", "",
			errcode.ErrFlagRepoQuery, "flag repo: list failed")
	}
	defer rows.Close()

	var flags []*domain.FeatureFlag
	for rows.Next() {
		flag, scanErr := scanFlagRow(rows)
		if scanErr != nil {
			return nil, ctxcancel.WrapOrInfra(scanErr, "List", "",
				errcode.ErrFlagRepoQuery, "flag repo: scan failed")
		}
		flags = append(flags, flag)
	}
	if err := rows.Err(); err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "List", "",
			errcode.ErrFlagRepoQuery, "flag repo: rows error")
	}
	return flags, nil
}

// Delete removes a feature flag by key if expectedVersion matches the stored
// version (CAS guard). Returns the deleted entity via DELETE...RETURNING.
// Returns ErrFlagNotFound if the key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
//
// CAS flow: rowsAffected==0 → probe GetByKey:
//   - exists → ErrVersionConflict (409)
//   - not found → ErrFlagNotFound (404)
func (r *FlagRepository) Delete(ctx context.Context, key string, expectedVersion int) (*domain.FeatureFlag, error) {
	const sql = `DELETE FROM feature_flags WHERE key=$1 AND version=$2 RETURNING ` + flagColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	flag, scanErr := scanFlagRow(db.QueryRow(ctx, sql, key, expectedVersion))
	if scanErr != nil {
		if cancelErr := ctxcancel.Wrap(scanErr, "Delete", "key="+key); cancelErr != nil {
			return nil, cancelErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, r.resolveUpdateConflict(ctx, "Delete", key)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrFlagRepoQuery, "flag repo query failed", scanErr,
			errcode.WithInternal(fmt.Sprintf("flag repo: Delete scan error key=%s", key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	return flag, nil
}

// Toggle atomically sets the enabled state and increments version by 1
// if expectedVersion matches the stored version (CAS guard).
// It does NOT overwrite rollout_percentage or description.
// Returns the updated flag via RETURNING clause.
// Returns ErrFlagNotFound if the key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
//
// ref: Unleash feature-environment-store.ts toggleEnvironment — dedicated toggle
// method prevents concurrent overwrites on unrelated fields.
//
// CAS flow: rowsAffected==0 → probe GetByKey:
//   - exists → ErrVersionConflict (409)
//   - not found → ErrFlagNotFound (404)
func (r *FlagRepository) Toggle(ctx context.Context, key string, expectedVersion int, enabled bool) (*domain.FeatureFlag, error) {
	const sql = `UPDATE feature_flags
		SET enabled=$1, version=version+1, updated_at=now()
		WHERE key=$2 AND version=$3
		RETURNING ` + flagColumns

	db, err := r.resolveWriteDB(ctx)
	if err != nil {
		return nil, err
	}
	flag, scanErr := scanFlagRow(db.QueryRow(ctx, sql, enabled, key, expectedVersion))
	if scanErr != nil {
		if cancelErr := ctxcancel.Wrap(scanErr, "Toggle", "key="+key); cancelErr != nil {
			return nil, cancelErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, r.resolveUpdateConflict(ctx, "Toggle", key)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrFlagRepoQuery, "flag repo query failed", scanErr,
			errcode.WithInternal(fmt.Sprintf("flag repo: Toggle scan error key=%s", key)),
			errcode.WithCategory(errcode.CategoryInfra),
		)
	}
	return flag, nil
}

// resolveUpdateConflict probes whether a key exists after an UPDATE/DELETE
// WHERE version=$N returned no rows. Three-way classification (PR464 P1.2 fix):
//
//   - Probe returns ErrFlagNotFound → key absent → 404
//   - Probe returns any other error (timeout, tx aborted, scan failure) → infra
//     fault — transparent passthrough as internal (do NOT masquerade as 404)
//   - Probe succeeds → key exists but version mismatch → 409 ErrVersionConflict
//
// ref: docs/reviews/PR-464 round-2 P1.2 (Kratos/Watermill/etcd: probe failure
// must not collapse into business not-found).
func (r *FlagRepository) resolveUpdateConflict(ctx context.Context, op, key string) error {
	_, probeErr := r.GetByKey(ctx, key)
	if probeErr != nil {
		notFound, infraErr := classifyProbeFailure(probeErr, errcode.ErrFlagNotFound, op, key, "feature_flag")
		if !notFound {
			return infraErr
		}
		// Confirmed key absent → 404.
		return errcode.Wrap(errcode.KindNotFound, errcode.ErrFlagNotFound,
			"flag not found", probeErr,
			errcode.WithInternal(fmt.Sprintf("flag repo: %s miss key=%s", op, key)),
			errcode.WithCategory(errcode.CategoryDomain),
		)
	}
	// Key exists but version did not match → 409.
	return cas.CheckVersionMatch(0, "feature_flag", key)
}
