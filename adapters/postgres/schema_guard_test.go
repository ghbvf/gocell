package postgres

import (
	"context"
	"errors"
	"io/fs"
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
	// Currently 44 migrations: 001-033 contiguous, plus 040-050 (intentional gap
	// per saga/L3 plan §R2 — 034-039 reserved for parallel PRs; goose sorts
	// by number, gaps are harmless).
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
	// for Model-A multi-tenancy isolation (EPIC #1337 PR-2a — renumbered 047→049→050 on merges).
	assert.Equal(t, int64(50), v,
		"expected version should be exactly 50 (current migration max — 050 accesscore_tenant_id)")
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
