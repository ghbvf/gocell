//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestVerifyExpectedVersion_Integration verifies that after applying all
// migrations, VerifyExpectedVersion returns nil (versions match).
func TestVerifyExpectedVersion_Integration(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	// Apply all migrations first.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// VerifyExpectedVersion should pass: DB version == FS max version.
	err = verifyExpectedVersionForTable(ctx, pool, testMigrationsFS(t), "schema_migrations")
	assert.NoError(t, err, "VerifyExpectedVersion should return nil after full Up()")
}

// TestDetectInvalidIndexes_WithInjectedInvalid verifies that DetectInvalidIndexes
// returns the name of an index that has been manually marked as invalid via
// a direct UPDATE on pg_index. This requires superuser (testcontainers PG
// default user is superuser).
func TestDetectInvalidIndexes_WithInjectedInvalid(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	// Apply migrations to create tables/indexes.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_invalid_idx")
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
	pool := emptyPool(t)

	ctx := context.Background()

	const tbl = "schema_migrations_ahead"

	// Apply all migrations.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), tbl)
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
	err = verifyExpectedVersionForTable(ctx, pool, testMigrationsFS(t), tbl)
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
	pool := emptyPool(t)

	ctx := context.Background()
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_lease_check")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 015")

	// Attempt to insert a pre-014 style row: claiming + NULL lease_id. The
	// post-N8 CHECK constraint must reject this with 23514 check_violation.
	// principal + occurred_at are NOT NULL (migration 044, applied by Up above);
	// supply them so the INSERT reaches the lease CHECK rather than tripping a
	// not-null violation first.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, principal, occurred_at, created_at, status, claimed_at, lease_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '{}', now(), now(), 'claiming', now(), NULL)`,
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
	pool := emptyPool(t)

	ctx := context.Background()
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_014_residue")
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
	pool := emptyPool(t)

	ctx := context.Background()
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_015_existing_bad")
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
	pool := emptyPool(t)

	ctx := context.Background()
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_015_update_path")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 015")

	// Insert a non-claiming row first so we have something to UPDATE. Use
	// status='pending' which has no constraint coupling. principal + occurred_at
	// are NOT NULL (migration 044, applied by Up above); supply them.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, principal, occurred_at, created_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '{}', now(), now(), 'pending')`,
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
	pool := emptyPool(t)

	ctx := context.Background()

	// Apply all migrations.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_lagged")
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
	err = verifyExpectedVersionForTable(ctx, pool, testMigrationsFS(t), "schema_migrations_lagged")
	require.Error(t, err, "should return error when DB is lagged")
	assert.Contains(t, err.Error(), "schema version mismatch",
		"error message should mention schema version mismatch")
}

// ---------------------------------------------------------------------------
// VerifyExpectedShape tests
// ---------------------------------------------------------------------------

// detailsContainKV is a helper that checks if any errcode.PublicDetail in
// details has the given key and string value.
func detailsContainKV(details []errcode.PublicDetail, key, value string) bool {
	for _, d := range details {
		if d.Key() != key {
			continue
		}
		s, ok := d.Value().(string)
		if ok && s == value {
			return true
		}
	}
	return false
}

// TestVerifyExpectedShape_AllColumnsPresent verifies that after all migrations
// are applied, VerifyExpectedShape returns nil (no shape drift).
func TestVerifyExpectedShape_AllColumnsPresent(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_happy")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = VerifyExpectedShape(ctx, pool)
	assert.NoError(t, err, "VerifyExpectedShape should return nil after full Up()")
}

// TestVerifyExpectedShape_MissingRequiredColumn verifies that dropping a
// required column causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with details naming the missing table and column.
func TestVerifyExpectedShape_MissingRequiredColumn(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_missing")
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
	assert.True(t, detailsContainKV(ec.Details, "table", "users"),
		"details must contain table=users; got %v", ec.Details)
	assert.True(t, detailsContainKV(ec.Details, "column", "authz_epoch"),
		"details must contain column=authz_epoch; got %v", ec.Details)
}

// TestVerifyExpectedShape_SeqIdentityDropped verifies the F5 (#1368) guard:
// weakening outbox_entries.seq from GENERATED ALWAYS AS IDENTITY to a plain
// bigint causes VerifyExpectedShape to return ErrAdapterPGSchemaShape. SET NOT
// NULL after DROP IDENTITY so the type + nullability checks still pass and the
// identity check is the one that fires (proving the new dimension, not the
// existing nullability dimension).
func TestVerifyExpectedShape_SeqIdentityDropped(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_seq_identity")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE outbox_entries ALTER COLUMN seq DROP IDENTITY`)
	require.NoError(t, execErr, "DROP IDENTITY must succeed (superuser in testcontainer)")
	_, execErr = pool.DB().Exec(ctx, `ALTER TABLE outbox_entries ALTER COLUMN seq SET NOT NULL`)
	require.NoError(t, execErr, "SET NOT NULL so type/nullability pass and identity is the sole fault")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must reject a non-IDENTITY seq column")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Contains(t, ec.Message, "GENERATED ALWAYS AS IDENTITY",
		"message must describe the identity fault")
	assert.True(t, detailsContainKV(ec.Details, "table", "outbox_entries"),
		"details must contain table=outbox_entries; got %v", ec.Details)
	assert.True(t, detailsContainKV(ec.Details, "column", "seq"),
		"details must contain column=seq; got %v", ec.Details)
}

// TestVerifyExpectedShape_ForbiddenColumnPresent verifies that adding a
// forbidden legacy column causes VerifyExpectedShape to return
// ErrAdapterPGSchemaShape with details naming the offending table and column.
func TestVerifyExpectedShape_ForbiddenColumnPresent(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_forbidden")
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
	assert.True(t, detailsContainKV(ec.Details, "table", "sessions"),
		"details must contain table=sessions; got %v", ec.Details)
	assert.True(t, detailsContainKV(ec.Details, "column", "access_token"),
		"details must contain column=access_token; got %v", ec.Details)
}

// ---------------------------------------------------------------------------
// VerifyNoInvalidIndexes tests
// ---------------------------------------------------------------------------

// TestVerifyNoInvalidIndexes_NoneInvalid verifies that after applying all
// migrations, VerifyNoInvalidIndexes returns nil (no invalid indexes).
func TestVerifyNoInvalidIndexes_NoneInvalid(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_valid_idx")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = VerifyNoInvalidIndexes(ctx, pool)
	assert.NoError(t, err, "VerifyNoInvalidIndexes should return nil when no invalid indexes exist")
}

// TestVerifyNoInvalidIndexes_DetectInvalid verifies that VerifyNoInvalidIndexes
// returns ErrAdapterPGInvalidIndex when at least one index is marked invalid.
func TestVerifyNoInvalidIndexes_DetectInvalid(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_invalidcheck")
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
		if a.Key() != "count" {
			continue
		}
		switch v := a.Value().(type) {
		case int:
			if int64(v) >= 1 {
				foundCount = true
			}
		case int64:
			if v >= 1 {
				foundCount = true
			}
		}
		if foundCount {
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_detect_empty")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_detect_invalid")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_probe_happy")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = InvalidIndexCheck(ctx, pool)
	assert.NoError(t, err, "InvalidIndexCheck should return nil when no invalid indexes exist")
}

// ---------------------------------------------------------------------------
// VerifyExpectedShape: multi-dimension wrong-shape tests
// ---------------------------------------------------------------------------

// extractDimensionDetail extracts the "dimension" PublicDetail value from an
// errcode.Error's Details slice. Returns "" if not found.
func extractDimensionDetail(ec *errcode.Error) string {
	for _, d := range ec.Details {
		if d.Key() != "dimension" {
			continue
		}
		if s, ok := d.Value().(string); ok {
			return s
		}
	}
	return ""
}

// TestVerifyExpectedShape_AllDimensionsHappy verifies that after all migrations
// are applied (including migration 023 adding CHECK constraints), VerifyExpectedShape
// returns nil.
func TestVerifyExpectedShape_AllDimensionsHappy(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_9dim_happy")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	err = VerifyExpectedShape(ctx, pool)
	assert.NoError(t, err, "VerifyExpectedShape should return nil after full Up() with all migrations")
}

// TestVerifyExpectedShape_DetectsMissingForeignKey verifies that dropping a
// foreign key constraint causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="foreign_key".
func TestVerifyExpectedShape_DetectsMissingForeignKey(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_fk")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_fk_ondel")
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
	assert.Contains(t, ec.Error(), "on_delete")
}

// TestVerifyExpectedShape_DetectsMissingUniqueIndex verifies that dropping a
// unique index causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="index".
func TestVerifyExpectedShape_DetectsMissingUniqueIndex(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_uidx")
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

// TestVerifyExpectedShape_DetectsIndexColumnsMismatch verifies that rebuilding
// an index with a wrong column order causes VerifyExpectedShape to return
// ErrAdapterPGSchemaShape with dimension="index_columns". This guards
// performance-critical indexes such as idx_audit_namespace_trace_id whose
// leading column determines whether a filter query can use the index.
func TestVerifyExpectedShape_DetectsIndexColumnsMismatch(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_idxcols")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Positive baseline: the migrated schema passes column-order checks.
	require.NoError(t, VerifyExpectedShape(ctx, pool),
		"VerifyExpectedShape must pass on the migrated schema")

	// Simulate column-order drift: drop and recreate idx_sessions_expires with
	// the columns reversed (expires_at, subject_id instead of just expires_at).
	// We add an extra column so the key-column set differs from the registry.
	_, execErr := pool.DB().Exec(ctx, `DROP INDEX idx_sessions_expires`)
	require.NoError(t, execErr, "DROP INDEX must succeed")
	// Recreate with wrong column order: subject_id leading instead of expires_at.
	_, execErr = pool.DB().Exec(ctx,
		`CREATE INDEX idx_sessions_expires ON sessions (subject_id, expires_at)`)
	require.NoError(t, execErr, "CREATE INDEX with wrong columns must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect index column mismatch")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "index_columns", extractDimensionDetail(ec),
		"details must contain dimension=index_columns; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingTrigger verifies that dropping a
// trigger causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="trigger".
func TestVerifyExpectedShape_DetectsMissingTrigger(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_trig")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `DROP TRIGGER effective_admin_invariant_on_role_assignments ON role_assignments`)
	require.NoError(t, execErr, "DROP TRIGGER must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing trigger")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "trigger", extractDimensionDetail(ec),
		"details must contain dimension=trigger; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingUsersTrigger verifies that dropping
// the users-table effective-admin trigger causes VerifyExpectedShape to return
// ErrAdapterPGSchemaShape with dimension="trigger". migration 024 installs a
// shared function on two tables; both trigger registrations must be guarded
// independently or a partial drop would silently disable the users-side
// invariant safety net.
func TestVerifyExpectedShape_DetectsMissingUsersTrigger(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_users_trig")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `DROP TRIGGER effective_admin_invariant_on_users ON users`)
	require.NoError(t, execErr, "DROP TRIGGER must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect missing users trigger")

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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_trig_dis")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE role_assignments DISABLE TRIGGER effective_admin_invariant_on_role_assignments`)
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_coltype")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_nullable")
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

// TestVerifyExpectedShape_DetectsMissingColumnDefault verifies that dropping the
// DEFAULT on a load-bearing column (projection_checkpoints.owner, omitted by the
// v1 upsert) causes VerifyExpectedShape to return ErrAdapterPGSchemaShape with
// dimension="column_default". This is the F1 guard: the write path relies on the
// default to satisfy NOT NULL, so its drift must be caught at startup.
func TestVerifyExpectedShape_DetectsMissingColumnDefault(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_default")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Positive baseline: the migrated schema satisfies the default registry.
	require.NoError(t, VerifyExpectedShape(ctx, pool),
		"VerifyExpectedShape must pass on the migrated schema (owner DEFAULT '' present)")

	// Drift: drop the owner default. The v1 upsert omits owner, so this would
	// make the first SaveOffset fail at write time on a NOT NULL violation.
	_, execErr := pool.DB().Exec(ctx, `ALTER TABLE projection_checkpoints ALTER COLUMN owner DROP DEFAULT`)
	require.NoError(t, execErr, "DROP DEFAULT must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect a dropped load-bearing column default")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Equal(t, "column_default", extractDimensionDetail(ec),
		"details must contain dimension=column_default; got %v", ec.Details)
}

// TestVerifyExpectedShape_DetectsMissingCheckConstraint verifies that dropping
// a CHECK constraint causes VerifyExpectedShape to return ErrAdapterPGSchemaShape
// with dimension="check".
func TestVerifyExpectedShape_DetectsMissingCheckConstraint(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_chk")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_pk")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_fn")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, execErr := pool.DB().Exec(ctx, `DROP FUNCTION effective_admin_invariant_fn CASCADE`)
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

// TestUsersMigration033_PasswordVersionNonNegative verifies that after
// migration 033 is applied, inserting a row with password_version = -1 into
// the users table is rejected by the DB CHECK constraint
// users_password_version_non_negative, while password_version = 0 is accepted.
//
// This locks the defense-in-depth invariant added by issue #940 P2-1: the DB
// refuses any write that would allow a negative sentinel value to persist —
// eliminating the silent implicit assumption that PasswordVersion >= 0.
//
// RED until migration 033 exists: without the constraint, the -1 INSERT
// succeeds and require.Error fires.
func TestUsersMigration033_PasswordVersionNonNegative(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_033_pw_version")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "all migrations must apply cleanly through 033")

	// password_version = 0 must succeed (NewUser baseline).
	_, execErr := pool.DB().Exec(
		ctx, `
		INSERT INTO users
			(id, tenant_id, username, email, password_hash, password_version,
			 creation_source, status, authz_epoch, created_at, updated_at)
		VALUES
			($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, 0,
			 'identity', 'active', 1, now(), now())`,
		"00000000-0000-0000-0033-000000000001",
		"alice_pw0",
		"alice_pw0@example.com",
		"$2a$12$dummy",
	)
	require.NoError(t, execErr, "password_version=0 must be accepted by DB")

	// password_version = -1 must be rejected by users_password_version_non_negative.
	_, execErr = pool.DB().Exec(
		ctx, `
		INSERT INTO users
			(id, tenant_id, username, email, password_hash, password_version,
			 creation_source, status, authz_epoch, created_at, updated_at)
		VALUES
			($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, -1,
			 'identity', 'active', 1, now(), now())`,
		"00000000-0000-0000-0033-000000000002",
		"alice_pwminus1",
		"alice_pwminus1@example.com",
		"$2a$12$dummy",
	)
	require.Error(t, execErr, "password_version=-1 must be rejected by DB CHECK constraint")
	assert.Contains(t, execErr.Error(), "users_password_version_non_negative",
		"error must surface the constraint name for ops triage")
}

// TestDetectInvalidIndexes_StillReportsOrphanWithProgressFilterAdded verifies
// that the LEFT JOIN pg_stat_progress_create_index added to the
// DetectInvalidIndexes query does not suppress orphan invalid indexes (i.e.,
// indexes with indisvalid=false that have no in-progress CONCURRENTLY build).
// This is a live integration test: it injects an invalid index into a real
// container and asserts the index is still returned.
func TestDetectInvalidIndexes_StillReportsOrphanWithProgressFilterAdded(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_inprogress_filter")
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

// TestVerifyExpectedShape_DetectsWrongFKLocalColumns verifies that the guard
// validates the FK's LOCAL constrained columns (conkey), not just the referenced
// columns (review F6). It degrades the composite tenant FK
// role_assignments(tenant_id, user_id) → users(tenant_id, id) to a single-column
// user_id → users(id), dropping tenant_id from the local set — the exact drift
// that would silently remove the DB-layer cross-tenant isolation. The guard must
// surface a foreign_key mismatch tagged as a local-column drift.
func TestVerifyExpectedShape_DetectsWrongFKLocalColumns(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_shape_fk_localcols")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	_, err = pool.DB().Exec(ctx, `ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_user_id_fkey`)
	require.NoError(t, err)
	// Re-add WITHOUT tenant_id in the local column set (references users(id) PK).
	_, err = pool.DB().Exec(ctx,
		`ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_user_id_fkey `+
			`FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE`)
	require.NoError(t, err, "recreate FK without tenant_id local column must succeed")

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must detect the dropped tenant_id local column")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code)
	assert.Equal(t, "foreign_key", extractDimensionDetail(ec))
	assert.Contains(t, ec.Error(), "local columns",
		"must surface the local-column drift specifically (not just a ref-column mismatch)")
}

// ---------------------------------------------------------------------------
// Migration 050 up-down-up idempotency and DestructiveDownPermit rejection (U12)
// ---------------------------------------------------------------------------

// pgColumnExists reports whether table.column exists in the current schema.
func pgColumnExists(t *testing.T, pool *Pool, table, column string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.DB().QueryRow(context.Background(), `
SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
)`, table, column).Scan(&exists))
	return exists
}

// assertMigrationUpDownUpIdempotent exercises migration targetVersion's destructive
// forward-rebuild up-down-up cycle and asserts it is idempotent.
//
// It loads a TRUNCATED migration FS (only migrations ≤ targetVersion) so
// targetVersion is the HIGHEST migration — i.e. the one a single Down rolls back.
// This pins the test to its named target regardless of higher migrations added
// later (#1622 F5: once migration 052 existed, a full-FS single Down rolled back
// 052 — not the named 050/051 — silently dropping the destructive-rebuild coverage
// while the test still reported green).
//
// The cycle is verified through the migration's signature column (the column
// targetVersion adds): present after Up → GONE after the single Down (proving the
// Down targeted exactly targetVersion, not a higher migration) → present again
// after re-Up. The full VerifyExpectedShape is NOT usable here because it pins the
// LATEST shape (including migration 052 RLS), which a truncated FS stops short of;
// HEAD shape/RLS is covered by the dedicated VerifyExpectedShape positive tests.
func assertMigrationUpDownUpIdempotent(t *testing.T, targetVersion int64, sigTable, sigColumn string) {
	t.Helper()
	pool := emptyPool(t)
	ctx := context.Background()
	fsys := migrationsUpToFS(t, targetVersion)
	tracking := fmt.Sprintf("schema_migrations_%d_idem", targetVersion)

	m1, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m1.Up(ctx), "initial Up() through migration %d must succeed", targetVersion)
	require.True(t, pgColumnExists(t, pool, sigTable, sigColumn),
		"%s.%s must exist after Up() to migration %d", sigTable, sigColumn, targetVersion)

	// Down requires an explicit DestructiveDownPermit (the forward-rebuild gate).
	downPermit, dpErr := AllowDestructiveDown(
		fmt.Sprintf("migration %d up-down-up idempotency test", targetVersion),
	)
	require.NoError(t, dpErr)

	m2, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m2.Down(ctx, downPermit), "Down() of migration %d must succeed", targetVersion)
	require.False(t, pgColumnExists(t, pool, sigTable, sigColumn),
		"%s.%s must be GONE after the single Down rolled back exactly migration %d "+
			"(proves the Down targeted %d, not a higher migration)",
		sigTable, sigColumn, targetVersion, targetVersion)

	// Second Up pass: empty tables → no permit needed; re-applies targetVersion.
	m3, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m3.Up(ctx), "second Up() re-applying migration %d must succeed", targetVersion)
	require.True(t, pgColumnExists(t, pool, sigTable, sigColumn),
		"%s.%s must exist again after the up-down-up cycle through migration %d",
		sigTable, sigColumn, targetVersion)
}

// TestMigration050_UpDownUpIdempotency verifies migration 050's destructive
// users/roles/role_assignments rebuild (adding tenant_id) is up-down-up idempotent.
func TestMigration050_UpDownUpIdempotency(t *testing.T) {
	assertMigrationUpDownUpIdempotent(t, 50, "users", "tenant_id")
}

// TestMigration050_DestructiveDownPermitRejection verifies that Migrator.Down
// returns an error when no DestructiveDownPermit is supplied (nil permit), i.e.,
// the typed-permit gate is enforced for migration 050's destructive Down block.
func TestMigration050_DestructiveDownPermitRejection(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply all migrations.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_050_downpermit")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must succeed on a fresh DB")

	// Attempt Down without a permit: must be rejected.
	downErr := migrator.Down(ctx, nil)
	require.Error(t, downErr, "Down() without a permit must return an error")
	var ec *errcode.Error
	require.True(t, errors.As(downErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
		"error code must be ErrValidationFailed for a missing permit")
}

// ---------------------------------------------------------------------------
// Migration 051 up-down-up idempotency and DestructiveDownPermit rejection
// ---------------------------------------------------------------------------

// TestMigration051_UpDownUpIdempotency verifies migration 051's destructive
// config_entries/config_versions/feature_flags rebuild (adding tenant_id) is
// up-down-up idempotent. The truncated-FS helper pins the single Down to 051 even
// though migration 052 (RLS) now sits above it (#1622 F5).
func TestMigration051_UpDownUpIdempotency(t *testing.T) {
	assertMigrationUpDownUpIdempotent(t, 51, "config_entries", "tenant_id")
}

// TestMigration051_DestructiveDownPermitRejection verifies that Migrator.Down
// returns an error when no DestructiveDownPermit is supplied (nil permit), i.e.,
// the typed-permit gate is enforced for migration 051's destructive Down block.
func TestMigration051_DestructiveDownPermitRejection(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply all migrations.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_051_downpermit")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must succeed on a fresh DB")

	// Attempt Down without a permit: must be rejected.
	downErr := migrator.Down(ctx, nil)
	require.Error(t, downErr, "Down() without a permit must return an error")
	var ec *errcode.Error
	require.True(t, errors.As(downErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
		"error code must be ErrValidationFailed for a missing permit")
}

// ---------------------------------------------------------------------------
// Migration 053 up-down-up idempotency (RLS ENABLE + FORCE on users/roles/role_assignments)
// ---------------------------------------------------------------------------

// pgRLSEnabled reports whether the named table has both relrowsecurity AND
// relforcerowsecurity set (i.e. ENABLE + FORCE ROW LEVEL SECURITY).
func pgRLSEnabled(t *testing.T, pool *Pool, table string) bool {
	t.Helper()
	var enabled, forced bool
	err := pool.DB().QueryRow(context.Background(), `
SELECT c.relrowsecurity, c.relforcerowsecurity
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  n.nspname = current_schema()
  AND  c.relname  = $1`, table).Scan(&enabled, &forced)
	if err != nil {
		return false
	}
	return enabled && forced
}

// pgPolicyExists reports whether the named policy exists on the given table.
func pgPolicyExists(t *testing.T, pool *Pool, table, policy string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.DB().QueryRow(context.Background(), `
SELECT EXISTS (
    SELECT 1 FROM pg_policies
    WHERE schemaname = current_schema()
      AND tablename  = $1
      AND policyname = $2
)`, table, policy).Scan(&exists))
	return exists
}

// TestMigration053_UpDownUpIdempotency verifies that migration 053 (ENABLE +
// FORCE RLS + tenant_isolation policy on users/roles/role_assignments) is
// up-down-up idempotent. After each Up the three tables must have RLS enabled
// and the policy present; after Down they must not.
func TestMigration053_UpDownUpIdempotency(t *testing.T) {
	ctx := context.Background()
	pool := emptyPool(t)
	fsys := migrationsUpToFS(t, 53)
	tracking := "schema_migrations_053_idem"

	m1, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m1.Up(ctx), "initial Up() through migration 053 must succeed")

	for _, tbl := range []string{"users", "roles", "role_assignments"} {
		assert.True(t, pgRLSEnabled(t, pool, tbl),
			"%s must have FORCE RLS after migration 053 Up", tbl)
		assert.True(t, pgPolicyExists(t, pool, tbl, "tenant_isolation"),
			"%s must have tenant_isolation policy after migration 053 Up", tbl)
	}

	downPermit, err := AllowDestructiveDown("migration 053 up-down-up idempotency test")
	require.NoError(t, err)

	m2, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m2.Down(ctx, downPermit), "Down() of migration 053 must succeed")

	for _, tbl := range []string{"users", "roles", "role_assignments"} {
		assert.False(t, pgRLSEnabled(t, pool, tbl),
			"%s must NOT have FORCE RLS after migration 053 Down", tbl)
		assert.False(t, pgPolicyExists(t, pool, tbl, "tenant_isolation"),
			"%s must NOT have tenant_isolation policy after migration 053 Down", tbl)
	}

	m3, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m3.Up(ctx), "second Up() re-applying migration 053 must succeed")

	for _, tbl := range []string{"users", "roles", "role_assignments"} {
		assert.True(t, pgRLSEnabled(t, pool, tbl),
			"%s must have FORCE RLS again after migration 053 re-Up", tbl)
		assert.True(t, pgPolicyExists(t, pool, tbl, "tenant_isolation"),
			"%s must have tenant_isolation policy again after migration 053 re-Up", tbl)
	}
}

// ---------------------------------------------------------------------------
// Migration 054 up-down-up idempotency (sessions.tenant_id column + composite FK)
// ---------------------------------------------------------------------------

// TestMigration054_UpDownUpIdempotency verifies that migration 054 (adds
// sessions.tenant_id carrier column and swaps to a composite same-tenant FK)
// is up-down-up idempotent. The truncated-FS helper pins the single Down to
// migration 054.
func TestMigration054_UpDownUpIdempotency(t *testing.T) {
	ctx := context.Background()
	pool := emptyPool(t)
	fsys := migrationsUpToFS(t, 54)
	tracking := "schema_migrations_054_idem"

	m1, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m1.Up(ctx), "initial Up() through migration 054 must succeed")
	require.True(t, pgColumnExists(t, pool, "sessions", "tenant_id"),
		"sessions.tenant_id must exist after migration 054 Up")

	downPermit, err := AllowDestructiveDown("migration 054 up-down-up idempotency test")
	require.NoError(t, err)

	m2, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m2.Down(ctx, downPermit), "Down() of migration 054 must succeed")
	require.False(t, pgColumnExists(t, pool, "sessions", "tenant_id"),
		"sessions.tenant_id must be GONE after migration 054 Down "+
			"(proves the Down targeted 054, not a higher migration)")

	m3, err := newMigratorForTable(pool, fsys, tracking)
	require.NoError(t, err)
	require.NoError(t, m3.Up(ctx), "second Up() re-applying migration 054 must succeed")
	require.True(t, pgColumnExists(t, pool, "sessions", "tenant_id"),
		"sessions.tenant_id must exist again after the up-down-up cycle through migration 054")
}
