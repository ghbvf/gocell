//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestSchemaGuard_Migration017_Users_TableAndIndexes verifies that migration 017
// creates the users table with the expected columns and UNIQUE indexes.
//
// ref: adapters/postgres/migrations/017_users.sql
// ref: cells/accesscore/internal/domain/user.go (UserStatus / UserSource consts)
func TestSchemaGuard_Migration017_Users_TableAndIndexes(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_017")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 019")

	// Assert: users table exists.
	var tableExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'users'
		)`).Scan(&tableExists)
	require.NoError(t, err)
	assert.True(t, tableExists, "users table must exist after migration 017")

	// Assert: all required columns present.
	wantCols := []string{
		"id", "username", "email", "password_hash",
		"password_reset_required", "status", "creation_source",
		"created_at", "updated_at",
	}
	for _, col := range wantCols {
		col := col
		var colExists bool
		err = pool.DB().QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'users' AND column_name = $1
			)`, col).Scan(&colExists)
		require.NoError(t, err)
		assert.Truef(t, colExists, "users must have column %q", col)
	}

	// Assert: idx_users_username UNIQUE index exists.
	var usernameIdxExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = 'users'
			  AND indexname = 'idx_users_username'
		)`).Scan(&usernameIdxExists)
	require.NoError(t, err)
	assert.True(t, usernameIdxExists, "idx_users_username must exist (UNIQUE)")

	// Assert: idx_users_email UNIQUE index exists.
	var emailIdxExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = 'users'
			  AND indexname = 'idx_users_email'
		)`).Scan(&emailIdxExists)
	require.NoError(t, err)
	assert.True(t, emailIdxExists, "idx_users_email must exist (UNIQUE)")

	// Assert: both indexes are UNIQUE via pg_indexes → pg_class.
	for _, idxName := range []string{"idx_users_username", "idx_users_email"} {
		idxName := idxName
		var isUnique bool
		err = pool.DB().QueryRow(ctx,
			`SELECT ix.indisunique
			 FROM pg_class c
			 JOIN pg_index ix ON ix.indrelid = c.oid
			 JOIN pg_class ci ON ci.oid = ix.indexrelid
			 WHERE c.relname = 'users' AND ci.relname = $1`,
			idxName).Scan(&isUnique)
		require.NoError(t, err)
		assert.Truef(t, isUnique, "%s must be a UNIQUE index", idxName)
	}
}

// TestSchemaGuard_Migration018_Sessions_TableAndIndexes verifies that migration 018
// creates the sessions table with the expected columns and indexes including the
// UNIQUE access_token constraint.
//
// ref: adapters/postgres/migrations/018_sessions.sql
// ref: cells/accesscore/internal/domain/session.go (Session.Version)
func TestSchemaGuard_Migration018_Sessions_TableAndIndexes(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_018")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 019")

	// Assert: sessions table exists.
	var tableExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'sessions'
		)`).Scan(&tableExists)
	require.NoError(t, err)
	assert.True(t, tableExists, "sessions table must exist after migration 018")

	// Assert: all required columns present.
	wantCols := []string{
		"id", "user_id", "access_token", "expires_at",
		"revoked_at", "created_at", "version",
	}
	for _, col := range wantCols {
		col := col
		var colExists bool
		err = pool.DB().QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'sessions' AND column_name = $1
			)`, col).Scan(&colExists)
		require.NoError(t, err)
		assert.Truef(t, colExists, "sessions must have column %q", col)
	}

	// Assert: idx_sessions_user_id index exists.
	var userIdxExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = 'sessions'
			  AND indexname = 'idx_sessions_user_id'
		)`).Scan(&userIdxExists)
	require.NoError(t, err)
	assert.True(t, userIdxExists, "idx_sessions_user_id must exist")

	// Assert: access_token has a UNIQUE index (enforced by UNIQUE column constraint).
	// PostgreSQL creates an implicit unique index named sessions_access_token_key
	// for UNIQUE column constraints; we verify uniqueness via pg_index.indisunique.
	var accessTokenIsUnique bool
	err = pool.DB().QueryRow(ctx,
		`SELECT ix.indisunique
		 FROM pg_class c
		 JOIN pg_index ix ON ix.indrelid = c.oid
		 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(ix.indkey)
		 WHERE c.relname = 'sessions' AND a.attname = 'access_token' AND ix.indisunique = true
		 LIMIT 1`).Scan(&accessTokenIsUnique)
	require.NoError(t, err)
	assert.True(t, accessTokenIsUnique, "access_token column must have a UNIQUE index")
}

// TestSchemaGuard_Migration019_Roles_TableAndIndexes verifies that migration 019
// creates the roles and role_assignments tables with the expected columns, and that
// idx_role_assignments_single_admin is a partial UNIQUE index with WHERE role_id='admin'.
//
// ref: adapters/postgres/migrations/019_roles.sql
// ref: PostgreSQL partial indexes (docs/indexes-partial.html)
// ref: jackc/pgx v5 pgconn PgError 23505 unique_violation
func TestSchemaGuard_Migration019_Roles_TableAndIndexes(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_019")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly through 019")

	// Assert: roles table exists.
	var rolesExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'roles'
		)`).Scan(&rolesExists)
	require.NoError(t, err)
	assert.True(t, rolesExists, "roles table must exist after migration 019")

	// Assert: roles columns.
	wantRoleCols := []string{"id", "name", "permissions", "created_at"}
	for _, col := range wantRoleCols {
		col := col
		var colExists bool
		err = pool.DB().QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'roles' AND column_name = $1
			)`, col).Scan(&colExists)
		require.NoError(t, err)
		assert.Truef(t, colExists, "roles must have column %q", col)
	}

	// Assert: role_assignments table exists.
	var assignmentsExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'role_assignments'
		)`).Scan(&assignmentsExists)
	require.NoError(t, err)
	assert.True(t, assignmentsExists, "role_assignments table must exist after migration 019")

	// Assert: role_assignments columns.
	wantAssignmentCols := []string{"user_id", "role_id", "assigned_at"}
	for _, col := range wantAssignmentCols {
		col := col
		var colExists bool
		err = pool.DB().QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'role_assignments' AND column_name = $1
			)`, col).Scan(&colExists)
		require.NoError(t, err)
		assert.Truef(t, colExists, "role_assignments must have column %q", col)
	}

	// Assert: idx_role_assignments_single_admin exists.
	var singleAdminIdxExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = 'role_assignments'
			  AND indexname = 'idx_role_assignments_single_admin'
		)`).Scan(&singleAdminIdxExists)
	require.NoError(t, err)
	assert.True(t, singleAdminIdxExists, "idx_role_assignments_single_admin must exist")

	// Assert: idx_role_assignments_single_admin is UNIQUE and PARTIAL (indpred IS NOT NULL).
	// indpred stores the WHERE clause predicate for partial indexes as a pg_node_tree;
	// a non-NULL indpred confirms the index is partial (WHERE role_id = 'admin').
	var isUnique bool
	var indpred *string // NULL for non-partial indexes
	err = pool.DB().QueryRow(ctx,
		`SELECT ix.indisunique, pg_get_expr(ix.indpred, ix.indrelid)
		 FROM pg_class c
		 JOIN pg_index ix ON ix.indrelid = c.oid
		 JOIN pg_class ci ON ci.oid = ix.indexrelid
		 WHERE c.relname = 'role_assignments'
		   AND ci.relname = 'idx_role_assignments_single_admin'`,
	).Scan(&isUnique, &indpred)
	require.NoError(t, err)
	assert.True(t, isUnique, "idx_role_assignments_single_admin must be UNIQUE")
	require.NotNil(t, indpred, "idx_role_assignments_single_admin must be a PARTIAL index (indpred IS NOT NULL)")
	assert.Contains(t, *indpred, "admin",
		"partial index predicate must reference 'admin' (WHERE role_id = 'admin')")
}
