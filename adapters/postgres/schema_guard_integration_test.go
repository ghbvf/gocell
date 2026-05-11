//go:build integration

package postgres

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestVerifyExpectedVersion_Integration verifies that after applying all
// migrations, VerifyExpectedVersion returns nil (versions match).
func TestVerifyExpectedVersion_Integration(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply all migrations first.
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// VerifyExpectedVersion should pass: DB version == FS max version.
	err = VerifyExpectedVersion(ctx, pool, testMigrationsFS(t), "schema_migrations")
	assert.NoError(t, err, "VerifyExpectedVersion should return nil after full Up()")
}

// TestDetectInvalidIndexes_WithInjectedInvalid verifies that DetectInvalidIndexes
// returns the name of an index that has been manually marked as invalid via
// a direct UPDATE on pg_index. This requires superuser (testcontainers PG
// default user is superuser).
func TestDetectInvalidIndexes_WithInjectedInvalid(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply migrations to create tables/indexes.
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_invalid_idx")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	// Verify no INVALID indexes before injection.
	before, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, before, "should have no invalid indexes before injection")

	// Inject an INVALID index by marking idx_outbox_pending_v2 as invalid.
	// We use pg_index system catalog directly (requires superuser).
	_, execErr := pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = false
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	require.NoError(t, execErr, "injecting invalid index must succeed (requires superuser)")

	// DetectInvalidIndexes should now report it. Names are schema-qualified
	// ("public.idx_foo") per the contract — multi-schema deployments rely on
	// this to avoid same-name false positives across schemas (B2-A-12).
	after, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err)
	assert.NotEmpty(t, after, "should detect the injected invalid index")
	var found bool
	for _, idx := range after {
		if idx.Index == "public.idx_outbox_pending_v2" {
			found = true
			assert.Equal(t, "public.outbox_entries", idx.Table,
				"invalid index Table must be schema-qualified")
			break
		}
	}
	assert.True(t, found,
		"invalid index list should contain schema-qualified public.idx_outbox_pending_v2; got %v", after)

	// Restore the index to valid state so container cleanup is clean.
	_, _ = pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = true
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
}

// TestVerifyExpectedVersion_DBAhead_Integration verifies that when the DB
// schema is ahead of the binary (DB version > FS max), VerifyExpectedVersion
// returns an error containing "schema version mismatch". This simulates a
// binary rollback without a corresponding migration rollback.
func TestVerifyExpectedVersion_DBAhead_Integration(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	const tbl = "schema_migrations_ahead"

	// Apply all migrations.
	migrator, err := NewMigrator(pool, testMigrationsFS(t), tbl)
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "initial Up() must succeed")

	// Determine the expected (FS max) version.
	expected, err := ExpectedVersion(testMigrationsFS(t))
	require.NoError(t, err)
	require.Greater(t, expected, int64(0), "test requires at least 1 migration")

	// Simulate a "binary rollback" scenario: DB has an extra version applied that
	// the current binary doesn't know about (expected + 1).
	_, execErr := pool.DB().Exec(ctx,
		"INSERT INTO "+tbl+" (version_id, is_applied, tstamp) VALUES ($1, true, NOW())",
		expected+1)
	require.NoError(t, execErr, "inserting extra version record must succeed")

	// VerifyExpectedVersion must now return a schema mismatch error (DB ahead).
	err = VerifyExpectedVersion(ctx, pool, testMigrationsFS(t), tbl)
	require.Error(t, err, "should return error when DB version is ahead of binary")
	assert.Contains(t, err.Error(), "schema version mismatch",
		"error message should mention schema version mismatch")
}

// TestOutboxClaimingLeaseCheckConstraint_RejectsNullLeaseInsert verifies the
// post-N8 invariant: the DB CHECK constraint
// `outbox_claiming_requires_lease` prevents any INSERT/UPDATE that combines
// status='claiming' with NULL lease_id. This is the single source of truth
// after N8 collapsed the previous startup probe into a DB-level constraint —
// rolling-deploy with a stale pre-014 binary directly hits 23514 check_violation
// instead of relying on a one-shot startup-time probe.
//
// ref: docs/architecture/202605051600-adr-pg-outbox-fencing.md cutover (N8)
// ref: riverqueue/river migration 004_pending_and_more.up.sql — DB-level
// invariants on state machine transitions are the canonical pattern.
func TestOutboxClaimingLeaseCheckConstraint_RejectsNullLeaseInsert(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_lease_check")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 015")

	// Attempt to insert a pre-014 style row: claiming + NULL lease_id. The
	// post-N8 CHECK constraint must reject this with 23514 check_violation.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, claimed_at, lease_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), 'claiming', now(), NULL)`,
		"00000000-0000-0000-0000-000000000001", "agg-1", "demo", "demo.event", "demo.topic",
		[]byte(`{}`), []byte(`{}`))

	require.Error(t, execErr, "DB CHECK must reject claiming + NULL lease_id")
	assert.Contains(t, execErr.Error(), "outbox_claiming_requires_lease",
		"error must surface the constraint name for ops triage")
}

// TestOutboxMigration014_AbortsOnClaimingResidue verifies the rolling-deploy
// fence built into 014_add_outbox_lease_id.sql: if any row is still in
// status='claiming' when migration 014 starts, the migration must abort with
// a row count rather than silently advancing the schema and leaving the
// pre-014 worker's mark/CAS chain unfenced.
//
// This locks the operational pre-requisite documented in the migration body
// and ADR `docs/architecture/202605051600-adr-pg-outbox-fencing.md` cutover §:
// drain the relay (or manually reset crash residue) before applying 014.
//
// Setup:
//   - Apply migrations through 013 (lease_id column does not yet exist).
//   - Insert one row with status='claiming' to simulate residue.
//   - Attempt migration 014 → must error with the residue row count.
//
// ref: docs/architecture/202605051600-adr-pg-outbox-fencing.md cutover §
func TestOutboxMigration014_AbortsOnClaimingResidue(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_014_residue")
	require.NoError(t, err)

	// Migrate up to but NOT including 014 — pre-014 schema lacks lease_id and
	// has no CHECK constraint, so a stale 'claiming' row is plain INSERT-able.
	const preLeaseVersion int64 = 13
	_, upErr := migrator.provider.UpTo(ctx, preLeaseVersion)
	require.NoError(t, upErr, "migrate to v13 must succeed")

	// Inject residue: a single row stuck in 'claiming'. This mirrors a worker
	// crash mid-publish, which the migration must refuse to silently fence over.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, claimed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), 'claiming', now())`,
		"00000000-0000-0000-0000-000000000014", "agg-residue", "demo", "demo.event", "demo.topic",
		[]byte(`{}`), []byte(`{}`))
	require.NoError(t, execErr, "pre-014 schema must accept claiming row")

	// Attempt migration 014 — DO block must RAISE EXCEPTION with the residue count.
	_, upErr = migrator.provider.UpTo(ctx, 14)
	require.Error(t, upErr, "014 must abort while claiming residue exists")
	assert.Contains(t, upErr.Error(), "outbox migration 014",
		"error must surface the migration name for ops triage")
	assert.Contains(t, upErr.Error(), "claiming",
		"error must surface the offending status for ops triage")
}

// TestOutboxMigration015_RejectsExistingClaimingNullLeaseRow verifies the
// fence built into 015_add_outbox_claiming_lease_check.sql: if any row in
// the table has status='claiming' AND lease_id IS NULL when 015 runs,
// adding the CHECK constraint must fail with check_violation. This locks
// the cutover invariant — operators MUST drain stale pre-014 binaries
// before applying 015, otherwise CAS lease fencing silently breaks.
//
// Setup:
//   - Apply migrations through 014 (lease_id column exists, no constraint yet).
//   - Insert one row with status='claiming' AND lease_id=NULL to simulate
//     a stale pre-014 binary writing through the post-014 schema.
//   - Attempt migration 015 → must error with check_violation on the constraint.
func TestOutboxMigration015_RejectsExistingClaimingNullLeaseRow(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_015_existing_bad")
	require.NoError(t, err)

	// Apply through 014: lease_id column exists, but the CHECK constraint
	// (introduced by 015) is not yet present.
	const preCheckVersion int64 = 14
	_, upErr := migrator.provider.UpTo(ctx, preCheckVersion)
	require.NoError(t, upErr, "migrate to v14 must succeed")

	// Inject a row that violates the about-to-be-added constraint: the
	// post-014 schema accepts it because no CHECK is yet present.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, claimed_at, lease_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), 'claiming', now(), NULL)`,
		"00000000-0000-0000-0000-000000000015", "agg-stale", "demo", "demo.event", "demo.topic",
		[]byte(`{}`), []byte(`{}`))
	require.NoError(t, execErr, "post-014 schema (no check yet) must accept claiming + NULL lease_id")

	// Attempt 015 — ALTER TABLE ADD CONSTRAINT validates existing rows and
	// must fail with check_violation referencing the named constraint.
	_, upErr = migrator.provider.UpTo(ctx, 15)
	require.Error(t, upErr, "015 must fail when existing rows violate the CHECK")
	assert.Contains(t, upErr.Error(), "outbox_claiming_requires_lease",
		"error must surface the constraint name for ops triage")
}

// TestOutboxMigration015_RejectsUpdateIntoClaimingNullLease verifies that
// after 015 is applied, an UPDATE that transitions a row INTO
// (status='claiming', lease_id=NULL) is rejected by the DB constraint.
// This complements TestOutboxClaimingLeaseCheckConstraint_RejectsNullLeaseInsert
// (which covers the INSERT path) — both are required to lock the
// invariant against the two write paths a pre-014 binary could exercise.
func TestOutboxMigration015_RejectsUpdateIntoClaimingNullLease(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_015_update_path")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 015")

	// Insert a non-claiming row first so we have something to UPDATE. Use
	// status='pending' which has no constraint coupling.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), 'pending')`,
		"00000000-0000-0000-0000-000000000016", "agg-update", "demo", "demo.event", "demo.topic",
		[]byte(`{}`), []byte(`{}`))
	require.NoError(t, execErr, "inserting pending row must succeed")

	// Attempt to UPDATE into the forbidden state. The CHECK constraint must
	// reject this with 23514 check_violation, mirroring the INSERT path.
	_, execErr = pool.DB().Exec(ctx,
		`UPDATE outbox_entries SET status = 'claiming', lease_id = NULL
		 WHERE id = $1`,
		"00000000-0000-0000-0000-000000000016")
	require.Error(t, execErr, "DB CHECK must reject UPDATE into claiming + NULL lease_id")
	assert.Contains(t, execErr.Error(), "outbox_claiming_requires_lease",
		"error must surface the constraint name for ops triage")
}

// TestVerifyExpectedVersion_DBLagged_Integration verifies that when the DB
// schema is behind the binary (DB version < FS max), VerifyExpectedVersion
// returns an error containing "schema version mismatch".
func TestVerifyExpectedVersion_DBLagged_Integration(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply all migrations.
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_lagged")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "initial Up() must succeed")

	// Determine the current max version so we can delete newer records.
	expected, err := ExpectedVersion(testMigrationsFS(t))
	require.NoError(t, err)
	require.Greater(t, expected, int64(3),
		"test requires at least 4 migrations to simulate lag")

	// Simulate lag: remove entries for versions > 3 from the tracking table.
	_, execErr := pool.DB().Exec(ctx,
		"DELETE FROM schema_migrations_lagged WHERE version_id > 3")
	require.NoError(t, execErr, "deleting version records should succeed")

	// VerifyExpectedVersion must now return a schema mismatch error.
	err = VerifyExpectedVersion(ctx, pool, testMigrationsFS(t), "schema_migrations_lagged")
	require.Error(t, err, "should return error when DB is lagged")
	assert.Contains(t, err.Error(), "schema version mismatch",
		"error message should mention schema version mismatch")
}

// ---------------------------------------------------------------------------
// VerifyExpectedShape tests
// ---------------------------------------------------------------------------

// attrsContainKV is a helper that checks if any slog.Attr in attrs has the
// given key and string value.
func attrsContainKV(attrs []slog.Attr, key, value string) bool {
	for _, a := range attrs {
		if a.Key == key && a.Value.String() == value {
			return true
		}
	}
	return false
}

// TestVerifyExpectedShape_AllColumnsPresent verifies that after all migrations
// are applied, VerifyExpectedShape returns nil (no shape drift).
func TestVerifyExpectedShape_AllColumnsPresent(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_happy")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = VerifyExpectedShape(ctx, pool)
	assert.NoError(t, err, "VerifyExpectedShape should return nil after full Up()")
}

// TestVerifyExpectedShape_MissingRequiredColumn verifies that dropping a
// required column causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with details naming the missing table and column.
func TestVerifyExpectedShape_MissingRequiredColumn(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_missing")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Drop a required column so VerifyExpectedShape detects the drift.
	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE users DROP COLUMN authz_epoch`)
	require.NoError(t, execErr, "DROP COLUMN must succeed (superuser in testcontainer)")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must return error when required column is missing")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code,
		"error code must be ErrAdapterPGSchemaShape")
	assert.Contains(t, ec.Message, "required column missing",
		"message must describe the fault")
	assert.True(t, attrsContainKV(ec.Details, "table", "users"),
		"details must contain table=users; got %v", ec.Details)
	assert.True(t, attrsContainKV(ec.Details, "column", "authz_epoch"),
		"details must contain column=authz_epoch; got %v", ec.Details)
}

// TestVerifyExpectedShape_ForbiddenColumnPresent verifies that adding a
// forbidden legacy column causes VerifyExpectedShape to return
// ErrAdapterPGSchemaShape with details naming the offending table and column.
func TestVerifyExpectedShape_ForbiddenColumnPresent(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_forbidden")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Re-introduce the legacy column to simulate a partial migration.
	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE sessions ADD COLUMN access_token TEXT`)
	require.NoError(t, execErr, "ADD COLUMN must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must return error when forbidden column exists")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code,
		"error code must be ErrAdapterPGSchemaShape")
	assert.Contains(t, ec.Message, "forbidden legacy column present",
		"message must describe the fault")
	assert.True(t, attrsContainKV(ec.Details, "table", "sessions"),
		"details must contain table=sessions; got %v", ec.Details)
	assert.True(t, attrsContainKV(ec.Details, "column", "access_token"),
		"details must contain column=access_token; got %v", ec.Details)
}

// ---------------------------------------------------------------------------
// VerifyNoInvalidIndexes tests
// ---------------------------------------------------------------------------

// TestVerifyNoInvalidIndexes_NoneInvalid verifies that after applying all
// migrations, VerifyNoInvalidIndexes returns nil (no invalid indexes).
func TestVerifyNoInvalidIndexes_NoneInvalid(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_valid_idx")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = VerifyNoInvalidIndexes(ctx, pool)
	assert.NoError(t, err, "VerifyNoInvalidIndexes should return nil when no invalid indexes exist")
}

// TestVerifyNoInvalidIndexes_DetectInvalid verifies that VerifyNoInvalidIndexes
// returns ErrAdapterPGInvalidIndex when at least one index is marked invalid.
func TestVerifyNoInvalidIndexes_DetectInvalid(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_invalidcheck")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Mark an existing index as invalid via pg_index (requires superuser).
	_, execErr := pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = false
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	require.NoError(t, execErr, "injecting invalid index must succeed (requires superuser)")

	// Restore after test.
	defer func() {
		_, _ = pool.DB().Exec(ctx,
			`UPDATE pg_index SET indisvalid = true
			 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	}()

	err = VerifyNoInvalidIndexes(ctx, pool)
	require.Error(t, err, "VerifyNoInvalidIndexes must return error when invalid indexes exist")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGInvalidIndex, ec.Code,
		"error code must be ErrAdapterPGInvalidIndex")
	assert.Contains(t, ec.Message, "invalid indexes",
		"message must describe the fault")
	// details should carry count >= 1
	var foundCount bool
	for _, a := range ec.Details {
		if a.Key == "count" && a.Value.Int64() >= 1 {
			foundCount = true
			break
		}
	}
	assert.True(t, foundCount, "details must contain count >= 1; got %v", ec.Details)
}

// ---------------------------------------------------------------------------
// DetectInvalidIndexes tests
// ---------------------------------------------------------------------------

// TestDetectInvalidIndexes_Empty verifies that after applying all migrations,
// DetectInvalidIndexes returns an empty slice.
func TestDetectInvalidIndexes_Empty(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_detect_empty")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	indexes, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, indexes, "DetectInvalidIndexes should return empty slice after clean migration")
}

// TestDetectInvalidIndexes_WithInvalidIndex verifies that DetectInvalidIndexes
// returns the schema-qualified name when an index is marked invalid via
// pg_index. Requires superuser (testcontainers default postgres image).
func TestDetectInvalidIndexes_WithInvalidIndex(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_detect_invalid")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Mark idx_outbox_pending_v2 as invalid.
	_, execErr := pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = false
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	require.NoError(t, execErr, "injecting invalid index must succeed (requires superuser)")

	// Always restore so container cleanup is clean.
	defer func() {
		_, _ = pool.DB().Exec(ctx,
			`UPDATE pg_index SET indisvalid = true
			 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	}()

	indexes, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err)
	require.NotEmpty(t, indexes, "DetectInvalidIndexes must return the injected invalid index")

	var found bool
	for _, idx := range indexes {
		if idx.Index == "public.idx_outbox_pending_v2" {
			found = true
			assert.Equal(t, "public.outbox_entries", idx.Table,
				"Table field must be schema-qualified")
			break
		}
	}
	assert.True(t, found,
		"result must contain public.idx_outbox_pending_v2; got %v", indexes)
}

// ---------------------------------------------------------------------------
// InvalidIndexCheck (readyz probe) happy-path test
// ---------------------------------------------------------------------------

// TestInvalidIndexCheck_NoInvalidIndexes verifies that InvalidIndexCheck (the
// /readyz probe wrapper) returns nil when no invalid indexes are present.
func TestInvalidIndexCheck_NoInvalidIndexes(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_probe_happy")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = InvalidIndexCheck(ctx, pool)
	assert.NoError(t, err, "InvalidIndexCheck should return nil when no invalid indexes exist")
}

// ---------------------------------------------------------------------------
// VerifyExpectedShape: multi-dimension wrong-shape tests
// ---------------------------------------------------------------------------

// extractDimensionDetail extracts the "dimension" slog.Attr value from an
// errcode.Error's Details slice. Returns "" if not found.
func extractDimensionDetail(ec *errcode.Error) string {
	for _, a := range ec.Details {
		if a.Key == "dimension" {
			return a.Value.String()
		}
	}
	return ""
}

// TestVerifyExpectedShape_AllDimensionsHappy verifies that after all migrations
// are applied (including migration 023 adding CHECK constraints), VerifyExpectedShape
// returns nil.
func TestVerifyExpectedShape_AllDimensionsHappy(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_9dim_happy")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = VerifyExpectedShape(ctx, pool)
	assert.NoError(t, err, "VerifyExpectedShape should return nil after full Up() with all migrations")
}

// TestVerifyExpectedShape_DetectsMissingForeignKey verifies that dropping a
// foreign key constraint causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="foreign_key".
func TestVerifyExpectedShape_DetectsMissingForeignKey(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_fk")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE sessions DROP CONSTRAINT sessions_subject_id_fkey`)
	require.NoError(t, execErr, "DROP CONSTRAINT must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing FK")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "foreign_key", extractDimensionDetail(ec),
		"details must contain dimension=foreign_key; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsWrongFKOnDeleteAction verifies that
// recreating a FK with a different ON DELETE action causes VerifyExpectedShape
// to surface the on-delete mismatch (covers verifyOneForeignKey OnDelete branch).
func TestVerifyExpectedShape_DetectsWrongFKOnDeleteAction(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_fk_ondel")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Drop the cascade FK and re-add it with NO ACTION (default).
	_, err = pool.DB().Exec(ctx, `ALTER TABLE sessions DROP CONSTRAINT sessions_subject_id_fkey`)
	require.NoError(t, err)
	_, err = pool.DB().Exec(ctx,
		`ALTER TABLE sessions ADD CONSTRAINT sessions_subject_id_fkey FOREIGN KEY (subject_id) REFERENCES users(id)`)
	require.NoError(t, err, "recreate FK without ON DELETE clause must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect on-delete drift")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code)
	assert.Equal(t, "foreign_key", extractDimensionDetail(ec))
	assert.Contains(t, ec.InternalMessage, "on_delete")
}

// TestVerifyExpectedShape_DetectsMissingUniqueIndex verifies that dropping a
// unique index causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="index".
func TestVerifyExpectedShape_DetectsMissingUniqueIndex(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_uidx")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `DROP INDEX idx_sessions_jti`)
	require.NoError(t, execErr, "DROP INDEX must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing unique index")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "index", extractDimensionDetail(ec),
		"details must contain dimension=index; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingTrigger verifies that dropping a
// trigger causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="trigger".
func TestVerifyExpectedShape_DetectsMissingTrigger(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_trig")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `DROP TRIGGER last_admin_protected ON role_assignments`)
	require.NoError(t, execErr, "DROP TRIGGER must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing trigger")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "trigger", extractDimensionDetail(ec),
		"details must contain dimension=trigger; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsDisabledTrigger verifies that disabling a
// trigger causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="trigger_enabled".
func TestVerifyExpectedShape_DetectsDisabledTrigger(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_trig_dis")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE role_assignments DISABLE TRIGGER last_admin_protected`)
	require.NoError(t, execErr, "DISABLE TRIGGER must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect disabled trigger")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "trigger_enabled", extractDimensionDetail(ec),
		"details must contain dimension=trigger_enabled; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsWrongColumnType verifies that changing a
// column's type causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="column_type".
func TestVerifyExpectedShape_DetectsWrongColumnType(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_coltype")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx,
		`ALTER TABLE users ALTER COLUMN authz_epoch TYPE INTEGER USING authz_epoch::INTEGER`)
	require.NoError(t, execErr, "ALTER COLUMN TYPE must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect column type change")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "column_type", extractDimensionDetail(ec),
		"details must contain dimension=column_type; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsNullableColumn verifies that dropping NOT NULL
// from a column causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="column_nullability".
func TestVerifyExpectedShape_DetectsNullableColumn(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_nullable")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE users ALTER COLUMN status DROP NOT NULL`)
	require.NoError(t, execErr, "DROP NOT NULL must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect nullable column")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "column_nullability", extractDimensionDetail(ec),
		"details must contain dimension=column_nullability; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingCheckConstraint verifies that dropping
// a CHECK constraint causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="check".
func TestVerifyExpectedShape_DetectsMissingCheckConstraint(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_chk")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_chk`)
	require.NoError(t, execErr, "DROP CONSTRAINT must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing CHECK constraint")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "check", extractDimensionDetail(ec),
		"details must contain dimension=check; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingPrimaryKey verifies that dropping the
// primary key causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="primary_key".
func TestVerifyExpectedShape_DetectsMissingPrimaryKey(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_pk")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// CASCADE because sessions.subject_id and role_assignments.user_id FKs
	// reference users(id); without CASCADE the PK drop fails with SQLSTATE 2BP01.
	// The test only asserts the PK-detection path; cascading FK drops are
	// inconsequential because VerifyExpectedShape runs PK checks before FK checks.
	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT users_pkey CASCADE`)
	require.NoError(t, execErr, "DROP CONSTRAINT users_pkey CASCADE must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing primary key")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "primary_key", extractDimensionDetail(ec),
		"details must contain dimension=primary_key; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingFunction verifies that dropping the
// PL/pgSQL function causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="function".
func TestVerifyExpectedShape_DetectsMissingFunction(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_shape_fn")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `DROP FUNCTION last_admin_protected_fn CASCADE`)
	require.NoError(t, execErr, "DROP FUNCTION CASCADE must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing function")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "function", extractDimensionDetail(ec),
		"details must contain dimension=function; got %v", ec.Details)
}

// ---------------------------------------------------------------------------
// DetectInvalidIndexes: in-progress filter tests
// ---------------------------------------------------------------------------

// TestDetectInvalidIndexes_StillReportsOrphanWithProgressFilterAdded verifies
// that the LEFT JOIN pg_stat_progress_create_index added to the
// DetectInvalidIndexes query does not suppress orphan invalid indexes (i.e.,
// indexes with indisvalid=false that have no in-progress CONCURRENTLY build).
// This is a live integration test: it injects an invalid index into a real
// container and asserts the index is still returned.
func TestDetectInvalidIndexes_StillReportsOrphanWithProgressFilterAdded(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_inprogress_filter")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Inject an invalid index (not in-progress).
	_, execErr := pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = false
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	require.NoError(t, execErr, "injecting invalid index must succeed")
	defer func() {
		_, _ = pool.DB().Exec(ctx,
			`UPDATE pg_index SET indisvalid = true
			 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	}()

	indexes, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err, "LEFT JOIN pg_stat_progress_create_index must not break the query")
	// The orphan invalid index (no pg_stat_progress entry) must still be reported.
	var found bool
	for _, idx := range indexes {
		if idx.Index == "public.idx_outbox_pending_v2" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"DetectInvalidIndexes must still report orphan invalid indexes; got %v", indexes)
}
