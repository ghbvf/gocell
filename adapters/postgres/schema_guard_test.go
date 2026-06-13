package postgres

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/migration"
)

// errDirFile implements fs.File and fs.ReadDirFile, returning error from ReadDir.
type errDirFile struct{ err error }

func (d errDirFile) Stat() (fs.FileInfo, error)           { return nil, d.err }
func (d errDirFile) Read(_ []byte) (int, error)           { return 0, d.err }
func (d errDirFile) Close() error                         { return nil }
func (d errDirFile) ReadDir(_ int) ([]fs.DirEntry, error) { return nil, d.err }

// readDirErrFS is an fs.FS whose root "." opens as an errDirFile.
type readDirErrFS struct{ err error }

func (r readDirErrFS) Open(name string) (fs.File, error) {
	if name == "." {
		return errDirFile(r), nil
	}
	return nil, r.err
}

// ---------------------------------------------------------------------------
// TestExpectedVersion — unit tests for the FS-scan helper
// ---------------------------------------------------------------------------

func TestExpectedVersion_FromEmbedFS(t *testing.T) {
	// Use the real embedded migrations FS to verify max version detection works.
	// This is a contract test: if a new migration is added, this test
	// automatically uses the new max.
	fsys := testMigrationsFS(t)
	v, err := ExpectedVersion(fsys)
	require.NoError(t, err)
	// Migration changelog (count is dynamic — verified by assert.Equal below):
	// 017/018/019 land users/sessions/roles schema for accesscore PG repos (S3+S5);
	// 020 adds audit_entries table for the ledger.Store PG backend; 021 adds the
	// (namespace, event_id) UNIQUE INDEX second-line idempotency guard;
	// 022 adds users.password_version for S6 narrow-scope CAS;
	// 023 adds users.status / creation_source CHECK constraints (S3F);
	// 024 installs the effective_admin_invariant trigger family (S4.0);
	// 025 drops sessions.authz_epoch_at_issue (S4b Batch 1C);
	// 026 restores sessions.authz_epoch_at_issue (S4d — ADR §0 A1 retracted, row is SoR);
	// 027 adds refresh_tokens.authz_epoch_at_issue (S4d — credential provenance for refresh chain);
	// 028 adds CHECK(authz_epoch>0) + DROP DEFAULT on the 3 epoch columns (S4d P2.a — '0=unset' hard DB invariant);
	// 029 creates the devices table (iotdevice devicecell L4 PG backend);
	// 030 creates the commands table (iotdevice L4 command queue PG backend);
	// 031 adds unique index on commands metadata->>'_idempotency_key' (DB-level TOCTOU-safe dedup);
	// 032 adds users.failed_login_count / last_failed_at / locked_until (accesscore auto-lockout);
	// 033 adds users.password_version >= 0 CHECK (#940 P2-1 defense-in-depth);
	// 040 creates saga_instances + saga_events for the PG saga journal (PR-04 / W6 step 4/10);
	// 041 extends saga_events.kind CHECK range from 1..9 to 1..10 (#1181 F3 — adds KindStepCompensationFailed);
	// 042 extends saga_events.kind 1..11 and saga_instances.status 1..8
	// (#1210 C6 — adds KindSagaCompensationFailed / StatusCompensationFailed);
	// 043 rebuilds audit_entries (DROP+CREATE) with 5 new NOT NULL columns
	// (subject_id / tenant_id / session_id / correlation_id / occurred_at) for
	// the 12-field canonical-JSON HMAC chain (#1228, supersedes PR #1218 W0 transition);
	// 044 adds principal (JSONB NOT NULL) and occurred_at (TIMESTAMPTZ NOT NULL) to
	// outbox_entries for the sealed-construction principal-injection feature (#1229);
	// 045 creates projection_checkpoints for the CQRS projection harness PG CheckpointStore
	// (#1174 / W10 PR-02 — owner column reserved, v1 unread/unwritten);
	// 046 creates reconcile_leases for the kernel/reconcile LeaderElector PG backend
	// (#661 / PR-A6 — holder + monotonic fencing epoch + lease window);
	// 047 adds audit_entries.trace_id (TEXT NOT NULL) for OTel correlation (#1048 Batch C);
	// 048 creates idx_audit_namespace_trace_id CONCURRENTLY (split from 047 per
	// migrations/README.md rule 1 — large-table indexes must use CONCURRENTLY);
	// 049 adds outbox_entries.seq (BIGINT GENERATED ALWAYS AS IDENTITY) + idx_outbox_seq:
	// the monotonic stream position for the projection journal ReplaySource/Cursor
	// (#1368 / W10 PR-04c);
	// 050 rebuilds users/roles/role_assignments (DROP+CREATE) adding tenant_id TEXT NOT NULL
	// for Model-A multi-tenancy isolation (EPIC #1337 PR-2a — renumbered 047→049→050 on merges);
	// 051 rebuilds config_entries/config_versions/feature_flags (DROP+CREATE) adding
	// tenant_id TEXT NOT NULL for configcore multi-tenancy isolation (EPIC #1337 PR-2b, #1479);
	// 052 enables FORCE ROW LEVEL SECURITY + tenant_isolation policy on the configcore
	// tables (non-destructive DDL) for the PR-3a RLS backstop (EPIC #1337, #1341);
	// 053 enables FORCE ROW LEVEL SECURITY + tenant_isolation policy on the accesscore
	// tables users/roles/role_assignments (EPIC #1337 PR-3b, #1617);
	// 054 adds sessions.tenant_id TEXT NOT NULL DEFAULT '' + backfills from users table
	// for the sessions tenant carrier (EPIC #1337 PR-3b, #1617);
	// 055 rebuilds audit_entries per-(namespace, tenant) (DROP+CREATE — UNIQUE(namespace,
	// tenant_id, seq_no)) + FORCE RLS with the OR tenant_id='' system-rows policy
	// (EPIC #1337 #1618);
	// 056 adds cert_epoch/cert_expires_at/renewal_requested_epoch to devices +
	// devices_cert_epoch_positive CHECK for durable cert-renewal state (#1819);
	// 057 adds idx_devices_cert_expires_at CONCURRENTLY for renewal-candidate scan (#1819);
	// 058 creates projection_events (append-only durable projection journal —
	// global_seq IDENTITY PK + idx_projection_events_id) for the #1504 retained event store.
	// 059 creates the ABAC policies table (composite PK + string-coded JSONB rules) under
	// FORCE RLS tenant_isolation for the durable ABAC policy store (EPIC #1337 PR-8, #1346).
	// 060 adds policies.version INT NOT NULL DEFAULT 1 for optimistic-concurrency (CAS)
	// versioning of the ABAC policy aggregate (EPIC #1337 PR-9, #1347).
	// 061 replaces the permanent idempotency key index with a state-aware active-uniqueness
	// partial index on commands (status IN 1,2,3) for retry-release semantics (#1820).
	// 062 drops devices.renewal_requested_epoch (stateless cert-renewal producer, #1820).
	// 063 creates the global webhook_sources table (encrypted webhook source secret store,
	// NO tenant/RLS, NO plaintext column) for the persistent SourceStore backing (#1540).
	// 064 adds saga_events.global_seq BIGINT GENERATED ALWAYS AS IDENTITY + idx_saga_events_global_seq
	// for the ordered GlobalReader scan (EPIC #1609 PR-PG Batch 1, #1630).
	// 065 adds the role-scoped permissive SELECT policy audit_admin_read_all on
	// audit_entries and GRANTs SELECT to gocell_audit_admin for cross-tenant audit
	// reads (#1810). Both steps are wrapped in IF EXISTS guards — inert where the role
	// is absent. schema_guard.go verifyRLS expects this second policy ONLY when
	// gocell_audit_admin is provisioned, preserving the #1622 F1 invariant.
	assert.Equal(t, int64(65), v,
		"expected version should be exactly 65 (current migration max — 065_audit_admin_read_role)")
}

func TestExpectedVersion_SyntheticFS(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string][]byte
		wantMax int64
	}{
		{
			name: "single migration",
			files: map[string][]byte{
				"001_create_foo.sql": []byte("-- up"),
			},
			wantMax: 1,
		},
		{
			name: "multiple migrations picks max",
			files: map[string][]byte{
				"001_create_foo.sql": []byte("-- up"),
				"003_add_bar.sql":    []byte("-- up"),
				"007_alter_baz.sql":  []byte("-- up"),
			},
			wantMax: 7,
		},
		{
			name:    "empty FS returns 0",
			files:   map[string][]byte{},
			wantMax: 0,
		},
		{
			name: "non-sql files are ignored",
			files: map[string][]byte{
				"README.md":      []byte("docs"),
				"001_create.sql": []byte("-- up"),
			},
			wantMax: 1,
		},
		{
			name: "subdirectory entries are skipped",
			files: map[string][]byte{
				"subdir/nested.sql": []byte("-- up"),
				"005_real.sql":      []byte("-- up"),
			},
			wantMax: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := make(fstest.MapFS)
			for name, content := range tc.files {
				fsys[name] = &fstest.MapFile{Data: content}
			}
			got, err := ExpectedVersion(fsys)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMax, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedVersion — unit tests for the validation guard
// ---------------------------------------------------------------------------

// TestVerifyExpectedVersion_InvalidNamespace verifies that an invalid
// migration.Namespace is rejected before any DB interaction. The exported API
// takes a typed Namespace (not a free table string), so the table can never be
// invalid for a valid namespace — the only fail path is an invalid namespace.
func TestVerifyExpectedVersion_InvalidNamespace(t *testing.T) {
	tests := []struct {
		name string
		ns   migration.Namespace
	}{
		{name: "empty", ns: migration.Namespace("")},
		{name: "dash conversion-literal escape", ns: migration.Namespace("schema-migrations")},
		{name: "space", ns: migration.Namespace("schema migrations")},
		{name: "leading digit", ns: migration.Namespace("1pay")},
	}

	fsys := fstest.MapFS{
		"001_create.sql": &fstest.MapFile{Data: []byte("-- +goose Up")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// ns.Validate runs before opening any DB connection (pool=nil proves it).
			err := VerifyExpectedVersion(context.Background(), nil, fsys, tc.ns)
			require.Error(t, err, "invalid namespace should return error")
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec, "error should be an errcode.Error")
			assert.Equal(t, ErrAdapterPGSchemaMismatch, ec.Code)
		})
	}
}

// TestVerifyExpectedVersionForTable_InvalidTableName verifies the unexported
// in-package escape hatch still rejects an invalid table identifier (production
// reaches it only via VerifyExpectedVersion's trackingTableFor(ns)).
func TestVerifyExpectedVersionForTable_InvalidTableName(t *testing.T) {
	fsys := fstest.MapFS{
		"001_create.sql": &fstest.MapFile{Data: []byte("-- +goose Up")},
	}
	for _, tbl := range []string{"schema_migrations; DROP TABLE users", "schema-migrations", "1_schema_migrations"} {
		err := verifyExpectedVersionForTable(context.Background(), nil, fsys, tbl)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	}
}

// ---------------------------------------------------------------------------
// TestDetectInvalidIndexes — unit tests for the InvalidIndex type
// ---------------------------------------------------------------------------

// TestInvalidIndex_Fields verifies the InvalidIndex struct fields are
// accessible and zero-valued correctly (compile-time + basic sanity).
func TestInvalidIndex_Fields(t *testing.T) {
	idx := InvalidIndex{
		Index: "public.idx_outbox_pending_v2",
		Table: "public.outbox_entries",
	}
	assert.Equal(t, "public.idx_outbox_pending_v2", idx.Index)
	assert.Equal(t, "public.outbox_entries", idx.Table)

	var zero InvalidIndex
	assert.Empty(t, zero.Index)
	assert.Empty(t, zero.Table)
}

// ---------------------------------------------------------------------------
// TestExpectedVersion — error path: ReadDir fails
// ---------------------------------------------------------------------------

// TestExpectedVersion_ReadDirError verifies that ExpectedVersion propagates
// a ReadDir failure from the underlying fs.FS.
func TestExpectedVersion_ReadDirError(t *testing.T) {
	sentinel := errors.New("disk I/O error")
	fsys := readDirErrFS{err: sentinel}

	_, err := ExpectedVersion(fsys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_guard: read migration dir",
		"error must include context prefix")
	assert.ErrorIs(t, err, sentinel, "original error must be wrapped")
}

// TestExpectedVersion_RejectsNonGooseParseable verifies the STRICT behavior
// (#1089 / codex C2): a top-level .sql whose name goose cannot derive a version
// from is rejected fail-fast (naming the file), NOT silently skipped — so a
// malformed/misnamed external-cell migration cannot vanish from ExpectedVersion
// (and thus VerifyAll) while goose's collector also ignores it.
func TestExpectedVersion_RejectsNonGooseParseable(t *testing.T) {
	tests := []struct {
		name    string
		badFile string
	}{
		{name: "no numeric prefix", badFile: "create_foo.sql"},
		{name: "namespace-prefixed", badFile: "payment_001.sql"},
		{name: "int64 overflow prefix", badFile: "99999999999999999999_too_big.sql"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				tc.badFile:       &fstest.MapFile{Data: []byte("-- up")},
				"003_normal.sql": &fstest.MapFile{Data: []byte("-- up")},
			}
			_, err := ExpectedVersion(fsys)
			require.Error(t, err, "malformed migration filename must be rejected, not skipped")
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, ErrAdapterPGSchemaMismatch, ec.Code)
			assert.Contains(t, err.Error(), tc.badFile, "error must name the offending file")
		})
	}
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedVersion — error path: ExpectedVersion returns error
// ---------------------------------------------------------------------------

// TestVerifyExpectedVersion_ExpectedVersionError verifies that
// VerifyExpectedVersion propagates a failure from ExpectedVersion
// before ever touching the DB pool.
func TestVerifyExpectedVersion_ExpectedVersionError(t *testing.T) {
	sentinel := errors.New("disk I/O error")
	fsys := readDirErrFS{err: sentinel}

	// Valid namespace → passes ns.Validate + table validateIdentifier, then fails
	// at ExpectedVersion. pool=nil is intentional: we must NOT reach stdlib.OpenDBFromPool.
	err := VerifyExpectedVersion(context.Background(), nil, fsys, migration.PlatformNamespace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_guard: compute expected version",
		"error must include context prefix")
	assert.ErrorIs(t, err, sentinel, "original error must be wrapped")
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedShape_Required* — static membership checks on
// shapeRequiredColumns (no DB required).
// ---------------------------------------------------------------------------

// containsColumn returns true when expectedColumns contains an entry
// with the given table and column. The 9-dim refactor replaced the prior
// shapeRequiredColumns slice with a structured expectedColumns registry
// (type + nullability + …); these tests verify presence-only membership.
func containsColumn(table, column string) bool {
	for _, r := range expectedColumns {
		if r.Table == table && r.Column == column {
			return true
		}
	}
	return false
}

// TestVerifyExpectedShape_RequiresUsersPasswordVersion verifies that the S6
// narrow-scope CAS column is declared in the required-column list.
func TestVerifyExpectedShape_RequiresUsersPasswordVersion(t *testing.T) {
	assert.True(t, containsColumn("users", "password_version"),
		"expectedColumns must include users.password_version (S6 migration 022)")
}

// TestVerifyExpectedShape_RequiresConfigEntriesVersion verifies that the
// PR449-F7 maintenance entry for config_entries.version is declared.
func TestVerifyExpectedShape_RequiresConfigEntriesVersion(t *testing.T) {
	assert.True(t, containsColumn("config_entries", "version"),
		"expectedColumns must include config_entries.version (migration 004 carry-over)")
}

// TestVerifyExpectedShape_RequiresFeatureFlagsVersion verifies that the
// PR449-F7 maintenance entry for feature_flags.version is declared.
func TestVerifyExpectedShape_RequiresFeatureFlagsVersion(t *testing.T) {
	assert.True(t, containsColumn("feature_flags", "version"),
		"expectedColumns must include feature_flags.version (migration 008 carry-over)")
}

// TestVerifyExpectedShape_RequiresAuditEntriesTraceID verifies that the
// migration 047 trace_id column is declared in the required-column list.
// trace_id carries the OpenTelemetry trace id for OTel correlation (#1048 Batch C);
// it is NOT part of the HMAC chain (observability only).
func TestVerifyExpectedShape_RequiresAuditEntriesTraceID(t *testing.T) {
	assert.True(t, containsColumn("audit_entries", "trace_id"),
		"expectedColumns must include audit_entries.trace_id (migration 047 OTel correlation)")
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedShape_RequiredChecks028 — migration 028 CHECK constraints
// ---------------------------------------------------------------------------

// containsCheck returns true when expectedChecks contains an entry with the
// given table and constraint name.
func containsCheck(table, name string) bool {
	for _, c := range expectedChecks {
		if c.Table == table && c.Name == name {
			return true
		}
	}
	return false
}

// TestVerifyExpectedShape_RequiresAuthzEpochPositiveChecks verifies that all
// three CHECK constraints added by migration 028 are declared in expectedChecks.
// Each constraint enforces authz_epoch > 0 at the DB level as a hard invariant
// so that any application path that bypasses the domain layer is caught at the
// storage level (migration 028 / S4d P2.a).
func TestVerifyExpectedShape_RequiresAuthzEpochPositiveChecks(t *testing.T) {
	tests := []struct {
		table      string
		constraint string
	}{
		{
			table:      "users",
			constraint: "users_authz_epoch_positive",
		},
		{
			table:      "sessions",
			constraint: "sessions_authz_epoch_at_issue_positive",
		},
		{
			table:      "refresh_tokens",
			constraint: "refresh_tokens_authz_epoch_at_issue_positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.table+"/"+tc.constraint, func(t *testing.T) {
			assert.True(t,
				containsCheck(tc.table, tc.constraint),
				"expectedChecks must include %s.%s (migration 028 authz_epoch positive invariant)",
				tc.table, tc.constraint,
			)
		})
	}
}

// TestVerifyExpectedShape_RequiresLockoutColumns verifies that migration 032's
// three auto-lockout bookkeeping columns are declared in expectedColumns.
// authzmutate.Mutator.ApplyInTx(ActivateUser{}) calls
// domain.User.ResetFailedLogins() in its apply() and persists via repo.Update;
// without these columns the schema_guard would not catch a future migration
// that accidentally dropped them, and admin Unlock would silently leave
// stored counters non-zero (PR #585 review P1#3 / F5).
func TestVerifyExpectedShape_RequiresLockoutColumns(t *testing.T) {
	tests := []struct {
		column string
		typ    string
	}{
		{column: "failed_login_count", typ: "integer"},
		{column: "last_failed_at", typ: "timestamp with time zone"},
		{column: "locked_until", typ: "timestamp with time zone"},
	}
	for _, tc := range tests {
		t.Run("users/"+tc.column, func(t *testing.T) {
			assert.True(t,
				containsColumn("users", tc.column),
				"expectedColumns must include users.%s (migration 032 auto-lockout)",
				tc.column,
			)
		})
	}
}

// TestVerifyExpectedShape_RequiresLockoutCountPositiveCheck verifies that the
// CHECK constraint added by migration 032 is declared in expectedChecks.
// The constraint enforces failed_login_count >= 0 at the DB level so any
// application path that bypasses the domain layer (which clamps to
// maxFailedLoginCount via the in-memory aggregate) is still caught at storage.
func TestVerifyExpectedShape_RequiresLockoutCountPositiveCheck(t *testing.T) {
	assert.True(t,
		containsCheck("users", "users_failed_login_count_positive"),
		"expectedChecks must include users.users_failed_login_count_positive "+
			"(migration 032 auto-lockout counter non-negativity)",
	)
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedShape_051 — migration 051 full-shape membership checks
// ---------------------------------------------------------------------------

// containsPK returns true when expectedPKs contains an entry with the given
// table and columns (order-sensitive, using slices.Equal).
func containsPK(table string, columns []string) bool {
	for _, pk := range expectedPKs {
		if pk.Table == table && slices.Equal(pk.Columns, columns) {
			return true
		}
	}
	return false
}

// TestVerifyExpectedShape_Requires051ConfigEntriesColumns verifies that all
// load-bearing columns for the 051-rebuilt config_entries table are declared
// in expectedColumns, including the new tenant_id column and the full set of
// cipher columns from migration 010.
func TestVerifyExpectedShape_Requires051ConfigEntriesColumns(t *testing.T) {
	for _, col := range []string{
		"id", "tenant_id", "key", "value", "sensitive",
		"version", "created_at", "updated_at",
		"value_cipher", "value_key_id", "value_edk", "value_nonce",
	} {
		col := col
		t.Run("config_entries/"+col, func(t *testing.T) {
			assert.True(t, containsColumn("config_entries", col),
				"expectedColumns must include config_entries.%s (migration 051 rebuild)", col)
		})
	}
}

// TestVerifyExpectedShape_Requires051ConfigVersionsColumns verifies that all
// load-bearing columns for the 051-rebuilt config_versions table are declared
// in expectedColumns.
func TestVerifyExpectedShape_Requires051ConfigVersionsColumns(t *testing.T) {
	for _, col := range []string{
		"id", "tenant_id", "config_id", "version", "value", "sensitive",
		"published_at",
		"value_cipher", "value_key_id", "value_edk", "value_nonce",
	} {
		col := col
		t.Run("config_versions/"+col, func(t *testing.T) {
			assert.True(t, containsColumn("config_versions", col),
				"expectedColumns must include config_versions.%s (migration 051 rebuild)", col)
		})
	}
}

// TestVerifyExpectedShape_Requires051FeatureFlagsColumns verifies that all
// load-bearing columns for the 051-rebuilt feature_flags table are declared
// in expectedColumns.
func TestVerifyExpectedShape_Requires051FeatureFlagsColumns(t *testing.T) {
	for _, col := range []string{
		"id", "tenant_id", "key", "enabled", "rollout_percentage",
		"description", "version", "created_at", "updated_at",
	} {
		col := col
		t.Run("feature_flags/"+col, func(t *testing.T) {
			assert.True(t, containsColumn("feature_flags", col),
				"expectedColumns must include feature_flags.%s (migration 051 rebuild)", col)
		})
	}
}

// TestVerifyExpectedShape_Requires051PKs verifies that all three 051-rebuilt
// tables have their primary key declared in expectedPKs.
func TestVerifyExpectedShape_Requires051PKs(t *testing.T) {
	tests := []struct {
		table   string
		columns []string
	}{
		{"config_entries", []string{"id"}},
		{"config_versions", []string{"id"}},
		{"feature_flags", []string{"id"}},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			assert.True(t, containsPK(tc.table, tc.columns),
				"expectedPKs must include %s.%v (migration 051 rebuild)", tc.table, tc.columns)
		})
	}
}

// TestVerifyExpectedShape_Requires051FeatureFlagsRolloutCheck verifies that
// the rollout_percentage BETWEEN 0 AND 100 CHECK constraint for feature_flags
// is declared in expectedChecks (migration 051 DROP+CREATE rebuild preserves
// migration 009's rollout_percentage constraint).
func TestVerifyExpectedShape_Requires051FeatureFlagsRolloutCheck(t *testing.T) {
	assert.True(t,
		containsCheck("feature_flags", "feature_flags_rollout_percentage_range"),
		"expectedChecks must include feature_flags.feature_flags_rollout_percentage_range "+
			"(migration 051 rebuild — rollout_percentage BETWEEN 0 AND 100)",
	)
}

// rlsPolicyOK returns the well-formed tenant_isolation policy shape (as pg_policies
// renders migration 052) — the positive control for checkRLSPolicyShape.
func rlsPolicyOK() rlsPolicyRow {
	const pred = "(tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text))"
	return rlsPolicyRow{
		name:       "tenant_isolation",
		permissive: "PERMISSIVE",
		cmd:        "ALL",
		roles:      "public",
		qual:       pred,
		withCheck:  pred,
	}
}

// rlsPolicyWith returns a single-policy slice with one field mutated from the
// well-formed shape.
func rlsPolicyWith(mut func(*rlsPolicyRow)) []rlsPolicyRow {
	p := rlsPolicyOK()
	mut(&p)
	return []rlsPolicyRow{p}
}

// rlsPolicyWithSystemOK returns the audit_entries SystemRowsReadable variant
// (#1618) — the migration-055 tenant_isolation policy with the `OR tenant_id =
// ”` system-rows clause, as pg_policies renders `(A OR B)` (each side wrapped).
func rlsPolicyWithSystemOK() rlsPolicyRow {
	const pred = "((tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text)) OR (tenant_id = ''::text))"
	return rlsPolicyRow{
		name:       "tenant_isolation",
		permissive: "PERMISSIVE",
		cmd:        "ALL",
		roles:      "public",
		qual:       pred,
		withCheck:  pred,
	}
}

// rlsPolicySystemWith returns a single-policy slice with one field mutated from
// the audit SystemRowsReadable shape.
func rlsPolicySystemWith(mut func(*rlsPolicyRow)) []rlsPolicyRow {
	p := rlsPolicyWithSystemOK()
	mut(&p)
	return []rlsPolicyRow{p}
}

// auditAdminPolicyOK returns the well-formed audit_admin_read_all policy shape
// (migration 065, #1810) — the positive control for checkRLSPolicyShape when
// the gocell_audit_admin role is provisioned.
func auditAdminPolicyOK() rlsPolicyRow {
	return rlsPolicyRow{
		name:       "audit_admin_read_all",
		permissive: "PERMISSIVE",
		cmd:        "SELECT",
		roles:      "gocell_audit_admin",
		qual:       "true",
		withCheck:  "", // SELECT-only policy has no WITH CHECK
	}
}

// TestCheckRLSPolicyShape is the DB-free unit cover of verifyRLSPolicy's
// validation core (#1622 F1): every semantic weakening of the tenant_isolation
// policy that a name-presence-only check would have passed must be rejected, and
// the well-formed shape must be accepted.
func TestCheckRLSPolicyShape(t *testing.T) {
	tests := []struct {
		name string
		// systemReadable selects the audit_entries variant (#1618): the
		// SystemRowsReadable expectedRLS that accepts the `OR tenant_id = ''`
		// predicate. Default false = the strict config/accesscore shape.
		systemReadable bool
		// auditAdminPolicy, when non-empty, sets r.AuditAdminPolicy and enables
		// the two-policy branch of checkRLSPolicyShape.
		auditAdminPolicy string
		// auditAdminRolePresent passes the role-presence flag to checkRLSPolicyShape.
		auditAdminRolePresent bool
		policies              []rlsPolicyRow
		wantErr               bool
	}{
		{name: "well_formed", policies: []rlsPolicyRow{rlsPolicyOK()}, wantErr: false},
		{name: "no_policy", policies: nil, wantErr: true},
		{
			name: "extra_permissive_policy",
			policies: []rlsPolicyRow{rlsPolicyOK(), {
				name: "extra_visible", permissive: "PERMISSIVE", cmd: "SELECT", roles: "public", qual: "true",
			}},
			wantErr: true,
		},
		{name: "wrong_name", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.name = "other" }), wantErr: true},
		{name: "restrictive", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.permissive = "RESTRICTIVE" }), wantErr: true},
		{name: "cmd_not_all", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.cmd = "SELECT" }), wantErr: true},
		{name: "roles_not_public", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.roles = "gocell_app" }), wantErr: true},
		{name: "using_true", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.qual = "true" }), wantErr: true},
		// #1622 F1 round-2: the predicate is matched as a WHOLE equality, so these
		// weakenings that a mere `app.tenant_id` substring check let through are now
		// rejected:
		{
			// Binds the WRONG column — still mentions app.tenant_id, so a substring
			// check passed it, but it isolates on other_col, not tenant_id.
			name: "using_wrong_column",
			policies: rlsPolicyWith(func(p *rlsPolicyRow) {
				p.qual = "(other_col = NULLIF(current_setting('app.tenant_id'::text, true), ''::text))"
			}),
			wantErr: true,
		},
		{
			// Vacuous: `… OR true` is always true. Mentions app.tenant_id but the
			// anchored ^…$ match rejects the trailing OR.
			name: "using_or_true",
			policies: rlsPolicyWith(func(p *rlsPolicyRow) {
				p.qual = "((tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text)) OR true)"
			}),
			wantErr: true,
		},
		{
			// Missing NULLIF: the empty-string GUC no longer maps to NULL, so the
			// fail-closed (unset GUC → 0 rows) semantics are lost.
			name: "using_missing_nullif",
			policies: rlsPolicyWith(func(p *rlsPolicyRow) {
				p.qual = "(tenant_id = current_setting('app.tenant_id'::text, true))"
			}),
			wantErr: true,
		},
		{
			// WITH CHECK uses the same predicate funnel — a wrong-column write-side
			// guard must also be rejected (would allow cross-tenant INSERT).
			name: "with_check_wrong_column",
			policies: rlsPolicyWith(func(p *rlsPolicyRow) {
				p.withCheck = "(other_col = NULLIF(current_setting('app.tenant_id'::text, true), ''::text))"
			}),
			wantErr: true,
		},
		{name: "missing_with_check", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.withCheck = "" }), wantErr: true},
		{name: "with_check_true", policies: rlsPolicyWith(func(p *rlsPolicyRow) { p.withCheck = "true" }), wantErr: true},

		// audit_entries SystemRowsReadable variant (#1618): the `OR tenant_id =
		// ''` predicate is ACCEPTED only when SystemRowsReadable is set.
		{name: "system_well_formed", systemReadable: true, policies: []rlsPolicyRow{rlsPolicyWithSystemOK()}, wantErr: false},
		// The strict (config/accesscore) shape MUST REJECT the `OR tenant_id = ''`
		// clause — only audit opts into the wider predicate.
		{name: "strict_rejects_or_system", systemReadable: false, policies: []rlsPolicyRow{rlsPolicyWithSystemOK()}, wantErr: true},
		// Conversely the audit variant MUST still reject the plain (no-OR) shape:
		// audit_entries REQUIRES the OR clause (else system-row inserts 42501).
		{name: "system_rejects_plain", systemReadable: true, policies: []rlsPolicyRow{rlsPolicyOK()}, wantErr: true},
		// The audit variant must still reject `OR true` (vacuous) — the right
		// operand is pinned to `tenant_id = ''`, not an arbitrary truthy clause.
		{
			name:           "system_or_true",
			systemReadable: true,
			policies: rlsPolicySystemWith(func(p *rlsPolicyRow) {
				p.qual = "((tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text)) OR true)"
			}),
			wantErr: true,
		},
		// And reject `OR tenant_id = '<other>'` (a specific foreign tenant leak).
		{
			name:           "system_or_other_tenant",
			systemReadable: true,
			policies: rlsPolicySystemWith(func(p *rlsPolicyRow) {
				p.qual = "((tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text)) OR (tenant_id = 'other'::text))"
			}),
			wantErr: true,
		},
		// PG may render the audit predicate with a single outer paren rather than
		// the double-paren form returned by rlsPolicyWithSystemOK(). Both forms are
		// canonically correct; the regex must accept both.
		{
			name:           "system_well_formed_single_paren",
			systemReadable: true,
			policies: rlsPolicySystemWith(func(p *rlsPolicyRow) {
				p.qual = "(tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text) OR tenant_id = ''::text)"
				p.withCheck = "(tenant_id = NULLIF(current_setting('app.tenant_id'::text, true), ''::text) OR tenant_id = ''::text)"
			}),
			wantErr: false,
		},

		// ---------------------------------------------------------------------------
		// audit_admin_read_all policy variant (#1810, migration 065):
		// Two-policy branch — role present means the second policy is expected.
		// ---------------------------------------------------------------------------

		// (a) Role present + both policies well-formed → PASS.
		{
			name:                  "audit_admin_role_present_well_formed",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies:              []rlsPolicyRow{rlsPolicyWithSystemOK(), auditAdminPolicyOK()},
			wantErr:               false,
		},
		// (b) SYNTHETIC RED: role present but an UNEXPECTED extra permissive policy
		// is present (instead of the well-formed audit_admin_read_all) — MUST FAIL.
		// This is the security guard: adding any extra permissive policy to audit_entries
		// when gocell_audit_admin is provisioned is still rejected if it is not the
		// exact expected audit_admin_read_all policy.
		{
			name:                  "audit_admin_role_present_unexpected_extra_policy",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies: []rlsPolicyRow{
				rlsPolicyWithSystemOK(),
				{name: "sneaky_extra", permissive: "PERMISSIVE", cmd: "SELECT", roles: "public", qual: "true"},
			},
			wantErr: true, // unexpected policy name — security guard not weakened
		},
		// (c) Role absent → single policy expected; absent-role + absent-policy passes.
		{
			name:                  "audit_admin_role_absent_single_policy",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: false,
			policies:              []rlsPolicyRow{rlsPolicyWithSystemOK()},
			wantErr:               false,
		},
		// (d) SYNTHETIC RED: role absent but the admin policy is somehow present
		// (e.g., manual policy injection without the role) — still TWO policies with
		// role absent → count mismatch → FAIL. The guard rejects any extra permissive
		// policy on audit_entries when only one is expected.
		{
			name:                  "audit_admin_role_absent_extra_policy_present",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: false,
			policies:              []rlsPolicyRow{rlsPolicyWithSystemOK(), auditAdminPolicyOK()},
			wantErr:               true, // extra policy when role absent is rejected (#1622 F1)
		},
		// (e) Role present but audit admin policy has wrong cmd (INSERT instead of SELECT).
		{
			name:                  "audit_admin_wrong_cmd",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies: []rlsPolicyRow{rlsPolicyWithSystemOK(), func() rlsPolicyRow {
				p := auditAdminPolicyOK()
				p.cmd = "INSERT"
				return p
			}()},
			wantErr: true,
		},
		// (f) Role present but audit admin policy applies to PUBLIC (should be role-scoped).
		{
			name:                  "audit_admin_wrong_roles_public",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies: []rlsPolicyRow{rlsPolicyWithSystemOK(), func() rlsPolicyRow {
				p := auditAdminPolicyOK()
				p.roles = "public" // must be gocell_audit_admin, not public
				return p
			}()},
			wantErr: true,
		},
		// (g) Role present but audit admin policy has USING(false) — not true.
		{
			name:                  "audit_admin_using_not_true",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies: []rlsPolicyRow{rlsPolicyWithSystemOK(), func() rlsPolicyRow {
				p := auditAdminPolicyOK()
				p.qual = "false"
				return p
			}()},
			wantErr: true,
		},
		// (h) Role present but audit admin policy has a WITH CHECK clause (SELECT
		// policy must not have WITH CHECK — a WITH CHECK would restrict writes and
		// is architecturally unsound for a read-only policy).
		{
			name:                  "audit_admin_has_with_check",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies: []rlsPolicyRow{rlsPolicyWithSystemOK(), func() rlsPolicyRow {
				p := auditAdminPolicyOK()
				p.withCheck = "true"
				return p
			}()},
			wantErr: true,
		},
		// (i) Role present but audit admin policy is RESTRICTIVE (must be PERMISSIVE
		// so it OR-es with tenant_isolation rather than AND-ing).
		{
			name:                  "audit_admin_restrictive",
			systemReadable:        true,
			auditAdminPolicy:      "audit_admin_read_all",
			auditAdminRolePresent: true,
			policies: []rlsPolicyRow{rlsPolicyWithSystemOK(), func() rlsPolicyRow {
				p := auditAdminPolicyOK()
				p.permissive = "RESTRICTIVE"
				return p
			}()},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := expectedRLS{
				Table:              "feature_flags",
				Policy:             "tenant_isolation",
				SystemRowsReadable: tt.systemReadable,
				AuditAdminPolicy:   tt.auditAdminPolicy,
			}
			err := checkRLSPolicyShape(r, tt.policies, tt.auditAdminRolePresent)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "must wrap *errcode.Error")
			assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code,
				"a weakened RLS policy must surface as a schema-shape fault")
		})
	}
}
