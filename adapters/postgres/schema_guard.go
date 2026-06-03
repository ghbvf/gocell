package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Tables owned by the adapters/postgres migration set (in migration order).
// Append here when a new table is introduced by a migration file so that
// schema_guard documentation stays in sync with the embedded SQL.
//
//   - outbox_entries     (001/044/049)  transactional outbox for event relay
//                                 + 044_outbox_entries_principal.sql TRUNCATE+rebuild
//                                   adding principal (jsonb) + occurred_at (timestamptz)
//                                   NOT NULL columns for the sealed-construction
//                                   principal-injection wire envelope (#1229).
//                                 + 049_outbox_entries_seq.sql adding seq BIGINT
//                                   GENERATED ALWAYS AS IDENTITY + idx_outbox_seq
//                                   (projection ReplaySource/Cursor position, #1368).
//   - config_entries     (004)  cell configuration key-value store
//   - config_versions    (004)  immutable configuration version history
//   - refresh_tokens     (007)  append-only refresh token lineage
//   - feature_flags      (008)  flag definitions
//   - users              (017)  accesscore user identities
//                                 + users_status_chk, users_creation_source_chk (023 CHECK)
//                                 + effective_admin_invariant_on_users trigger (024)
//                                 + tenant_id TEXT NOT NULL (050 DROP+CREATE rebuild)
//                                   UNIQUE(tenant_id,username) / UNIQUE(tenant_id,email)
//                                   replacing global idx_users_username / idx_users_email;
//                                   support index UNIQUE(tenant_id,id) for role FK.
//   - sessions           (018)  accesscore session / JTI store
//                                 + authz_epoch_at_issue restored (026; ADR §A8 — row is SoR, claim was retracted)
//   - roles              (019)  accesscore role definitions
//                                 + tenant_id TEXT NOT NULL, PK becomes (tenant_id, id) (050)
//   - role_assignments   (019)  accesscore user-role grants
//                                 + effective_admin_invariant_on_role_assignments trigger (024)
//                                 + tenant_id TEXT NOT NULL, PK (tenant_id,user_id,role_id),
//                                   role FK references roles(tenant_id,id) composite (050)
//   - audit_entries      (020/043 + 047 (trace_id col) + 048 (trace_id index)
//                          + 051 (tenant keyset index)) tamper-evident audit ledger (per-namespace hash chain)
//                                 + 043_audit_entries_v2 DROP+CREATE rebuild adding
//                                   5 NOT NULL columns (subject_id / tenant_id /
//                                   session_id / correlation_id / occurred_at) for
//                                   the 12-field canonical-JSON HMAC chain.
//   - devices            (029)  examples/iotdevice devicecell PG repo (B2.B)
//                                 + devices_status_chk CHECK (status IN online/offline)
//   - commands           (030)  examples/iotdevice command queue PG adapter (B2.B)
//                                 + commands.device_id FK → devices(id) ON DELETE RESTRICT
//                                 + commands_status_chk, commands_attempt_chk
//                                 + idx_commands_idempotency_key UNIQUE partial (031)
//   - saga_instances     (040)  saga coordinator instance projection + lease fencing
//                                 + saga_instances_status_range, saga_instances_version_nonneg,
//                                   saga_instances_lease_paired CHECK
//                                 + idx_saga_instances_claimable partial index
//   - saga_events        (040)  append-only saga event log
//                                 + PK(instance_id, version) + FK→saga_instances(id) ON DELETE CASCADE
//                                 + saga_events_kind_range, saga_events_version_positive CHECK
//   - projection_checkpoints (045)  CQRS projection harness consumed-offset store
//                                 + PK(cell_id, projection_id)
//                                 + owner column reserved, write-guarded (v1 never writes it; reads harmless; ADR §Q5)
//
// Drift between this comment and verifyChecks/verifyIndexes/... registries is
// caught by archtest SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01.

// migrationVersionRe matches migration file names like "006_add_something.sql"
// and captures the numeric prefix.
var migrationVersionRe = regexp.MustCompile(`^(\d+)_`)

// ExpectedVersion scans the given fs.FS for .sql migration files and returns
// the maximum numeric prefix found. This represents the expected schema version
// that must be present in the database for the binary to start safely.
//
// ref: pressly/goose v3.27 Provider — migrations embedded in FS.
func ExpectedVersion(fsys fs.FS) (int64, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return 0, fmt.Errorf("schema_guard: read migration dir: %w", err)
	}

	var max int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationVersionRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		v, parseErr := strconv.ParseInt(m[1], 10, 64)
		if parseErr != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	return max, nil
}

// defaultSchemaTable is the goose migration tracking table used by GoCell.
// Must match the tableName passed to NewMigrator in production code.
const defaultSchemaTable = "schema_migrations"

// VerifyExpectedVersion compares the database's current goose schema version
// against the expected version derived from the embedded migration FS.
//
// tableName is the goose tracking table (pass "" to use the default
// "schema_migrations"). It must match the table used by NewMigrator.
//
// Returns ErrAdapterPGSchemaMismatch if:
//   - actual < expected: DB schema is behind the binary (migrations not run).
//   - actual > expected: binary is behind the DB (binary rollback without migration rollback).
//
// Returns nil when actual == expected.
//
// ref: pressly/goose v3.27 Provider.GetDBVersion — GetDBVersion reads max version
// from the goose version table (schema_migrations by default).
func VerifyExpectedVersion(ctx context.Context, pool *Pool, fsys fs.FS, tableName ...string) error {
	tbl := defaultSchemaTable
	if len(tableName) > 0 && tableName[0] != "" {
		tbl = tableName[0]
	}
	if err := validateIdentifier(tbl); err != nil {
		return err
	}

	expected, err := ExpectedVersion(fsys)
	if err != nil {
		return fmt.Errorf("schema_guard: compute expected version: %w", err)
	}

	// Open a *sql.DB via pgx stdlib adapter (same as Migrator) to use goose
	// Provider for reading the actual DB version.
	db := stdlib.OpenDBFromPool(pool.inner)
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			slog.Debug("schema_guard: close sql.DB", slog.Any("error", closeErr))
		}
	}()

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		fsys,
		goose.WithTableName(tbl),
	)
	if err != nil {
		return fmt.Errorf("schema_guard: create goose provider: %w", err)
	}

	actual, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("schema_guard: get DB version: %w", err)
	}

	if actual != expected {
		return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaMismatch,
			"schema version mismatch",
			errcode.WithDetails(errcode.PublicInt("db", actual), errcode.PublicInt("binary", expected)))
	}

	slog.Info("schema_guard: schema version matched",
		slog.Int64("version", actual))
	return nil
}

// InvalidIndex describes an index that is marked as invalid in pg_index.
// Invalid indexes can occur when CREATE INDEX CONCURRENTLY is interrupted.
type InvalidIndex struct {
	// Index is the qualified name of the invalid index (e.g. "public.idx_foo").
	Index string
	// Table is the qualified name of the table the index belongs to.
	Table string
}

// InvalidIndexCheck wraps DetectInvalidIndexes for use as a readyz probe
// (func(context.Context) error signature). Returns:
//
//   - nil when no invalid indexes exist
//   - the underlying query error (KindInternal) when DetectInvalidIndexes fails
//     — this is a real fault (connection, SQL error) and maps to "unhealthy"
//   - an errcode error when indisvalid=false rows are present. Invalid indexes
//     are a schema fault, so runtime/http/health.runOneProbe classifies this as
//     "unhealthy" and /readyz returns HTTP 503. Operators see the invalid-index
//     list in /readyz?verbose diagnostics and DROP the index manually.
//
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers return error.
func InvalidIndexCheck(ctx context.Context, pool *Pool) error {
	indexes, err := DetectInvalidIndexes(ctx, pool)
	if err != nil {
		return err
	}
	if len(indexes) == 0 {
		return nil
	}
	names := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		names = append(names, idx.Index)
	}
	return errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
		"schema_guard: invalid indexes detected",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("%d invalid index(es): %s", len(indexes), strings.Join(names, ", ")))),
		errcode.WithDetails(errcode.PublicInt("invalidCount", len(indexes))))
}

// VerifyExpectedShape checks that the post-migration column / table shape
// matches the binary's expectation across 9 structural dimensions:
// columns (type + nullability), primary keys, unique indexes, foreign keys
// (with ON DELETE action), non-unique indexes, triggers (enabled state +
// function), trigger functions, and CHECK constraints.
//
// Run **after** VerifyExpectedVersion (which gates migration version) —
// VerifyExpectedShape catches "version table says N but my migration's DDL
// never reached the column" drift, e.g. partial migration that did not abort,
// or a 3rd-party tool applying SQL out-of-band.
//
// ADR-credential §5.1.3 deployment playbook mandates these checks for the
// S3+S5 schema. Each fault returns ErrAdapterPGSchemaShape so operators see
// the precise dimension at fault.
func VerifyExpectedShape(ctx context.Context, pool *Pool) error {
	if err := verifyColumns(ctx, pool); err != nil {
		return err
	}
	if err := verifyDefaults(ctx, pool); err != nil {
		return err
	}
	if err := verifyForbiddenColumns(ctx, pool); err != nil {
		return err
	}
	if err := verifyPrimaryKeys(ctx, pool); err != nil {
		return err
	}
	if err := verifyIndexes(ctx, pool); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, pool); err != nil {
		return err
	}
	// Functions before triggers: a trigger depends on its function (DROP
	// FUNCTION ... CASCADE removes both), so reporting the function as the
	// root cause is more actionable than the cascaded trigger absence.
	if err := verifyFunctions(ctx, pool); err != nil {
		return err
	}
	if err := verifyTriggers(ctx, pool); err != nil {
		return err
	}
	if err := verifyChecks(ctx, pool); err != nil {
		return err
	}
	return nil
}

// requiredColumn pairs a (table, column) tuple for VerifyExpectedShape.
type requiredColumn struct {
	table  string
	column string
}

// expectedColumn extends requiredColumn with type and nullability expectations.
type expectedColumn struct {
	Table   string
	Column  string
	Type    string
	NotNull bool
	// Identity, when true, requires the column to be GENERATED ALWAYS AS IDENTITY
	// (pg_attribute.attidentity = 'a'). This is a load-bearing write contract for
	// outbox_entries.seq: the outbox writer omits seq and relies on auto-assign, and
	// ALWAYS (not BY DEFAULT) forbids a producer supplying its own position. A future
	// migration weakening it to a plain/BY-DEFAULT column would pass the type+nullability
	// checks but silently break the writer / open position injection (#1368 review F5).
	Identity bool
}

// expectedPK describes a table's primary key column set.
type expectedPK struct {
	Table   string
	Columns []string
}

// expectedDefault asserts a column's DEFAULT expression (as rendered by
// pg_get_expr). Only LOAD-BEARING defaults are registered: defaults a write
// path relies on by OMITTING the column from its INSERT. Most columns have
// defaults that are mere conveniences (the writer always supplies the value);
// those are NOT registered here. Default is the exact pg_get_expr rendering of
// the column's DEFAULT clause (an empty-string text default renders as the SQL
// empty literal cast to text).
type expectedDefault struct {
	Table   string
	Column  string
	Default string
}

// expectedFK describes a foreign key constraint.
//
// Both column sets are validated IN ORDER (slices.Equal, not set-equality):
// Columns is the local constrained set (pg_constraint.conkey) and RefColumns is
// the referenced set (confkey). Order matters for composite tenant FKs — e.g.
// role_assignments(tenant_id, user_id) → users(tenant_id, id) is a different
// (and security-meaningful) constraint than a column-swapped variant, so a drift
// that reorders the pair must be rejected (review F6).
type expectedFK struct {
	Table      string
	Constraint string
	Columns    []string // local constrained columns (conkey), in declaration order
	RefTable   string
	RefColumns []string // referenced columns (confkey), in declaration order
	OnDelete   string   // e.g. "a" = CASCADE, "r" = RESTRICT
}

// expectedIndex describes a named index (unique or non-unique).
// Columns lists the key column names in index key order (DDL order).
// For expression columns (e.g. functional indexes) use the sentinel "(expr)".
// INCLUDE columns are not listed here — only key columns.
type expectedIndex struct {
	Table   string
	Name    string
	Unique  bool
	Columns []string
}

// expectedTrigger describes a trigger with its enabled state and function.
type expectedTrigger struct {
	Table    string
	Name     string
	Function string
	Enabled  bool // tgenabled = 'O' means enabled
}

// expectedFunction names a PL/pgSQL function that must exist in the current schema.
type expectedFunction struct {
	Name string
}

// expectedCheck names a CHECK constraint on a table.
type expectedCheck struct {
	Table string
	Name  string
}

// pgTypeTSTZ is the PostgreSQL column type name for a timezone-aware timestamp.
// Extracted to satisfy go:S1192 (used in 16 expectedColumns entries).
const pgTypeTSTZ = "timestamp with time zone"

// queryErrFmt is the WithInternal detail format for failed schema-dimension
// SQL probes. Extracted to satisfy go:S1192 (used in 4 dimension probes).
const queryErrFmt = "query: %v"

// ---------------------------------------------------------------------------
// Expected shape registries (hardcoded per ADR-credential §5.1.3)
// ---------------------------------------------------------------------------

// expectedColumns is the authoritative column-type-nullability registry for
// the S3F-owned tables (users/sessions/roles/role_assignments), the
// auditcore-owned audit_entries table (020_audit_ledger.sql), and the
// outbox_entries relay table (001_create_outbox_entries.sql + 044 + 049).
var expectedColumns = []expectedColumn{
	// outbox_entries (001 + subsequent migrations + 044_outbox_entries_principal.sql)
	// Only the writer-supplied columns are registered; relay-internal columns
	// (status, attempts, lease_id, claimed_at, next_retry_at, published_at,
	// dead_at, last_error) are not enumerated here — they evolve independently
	// of the sealed-construction principal injection feature.
	// id is TEXT, not UUID: migration 003 widens it from UUID to TEXT in its Up
	// section ("support prefixed IDs evt-<uuid>/audit-<uuid>"; outbox.NewEntryID
	// returns a string). The UUID conversion in 003 lives in the Down (rollback)
	// section only, so the live forward schema is text.
	{Table: "outbox_entries", Column: "id", Type: "text", NotNull: true},
	{Table: "outbox_entries", Column: "aggregate_id", Type: "text", NotNull: true},
	{Table: "outbox_entries", Column: "aggregate_type", Type: "text", NotNull: true},
	{Table: "outbox_entries", Column: "event_type", Type: "text", NotNull: true},
	{Table: "outbox_entries", Column: "topic", Type: "text", NotNull: true},
	{Table: "outbox_entries", Column: "payload", Type: "jsonb", NotNull: true},
	{Table: "outbox_entries", Column: "metadata", Type: "jsonb", NotNull: false},
	{Table: "outbox_entries", Column: "created_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "outbox_entries", Column: "observability", Type: "jsonb", NotNull: false},
	{Table: "outbox_entries", Column: "principal", Type: "jsonb", NotNull: true},      // 044 NEW
	{Table: "outbox_entries", Column: "occurred_at", Type: pgTypeTSTZ, NotNull: true}, // 044 NEW
	// seq is GENERATED ALWAYS AS IDENTITY (implicitly NOT NULL) — the monotonic
	// stream position consumed by the projection ReplaySource/Cursor (049 / #1368).
	// Identity:true guards the GENERATED ALWAYS write contract (F5).
	{Table: "outbox_entries", Column: "seq", Type: "bigint", NotNull: true, Identity: true}, // 049 NEW
	// users (017_users.sql + 022_users_password_version.sql + 050_accesscore_tenant_id.sql)
	// 050 drops+recreates the table adding tenant_id TEXT NOT NULL as the second column.
	{Table: "users", Column: "id", Type: "uuid", NotNull: true},
	{Table: "users", Column: "tenant_id", Type: "text", NotNull: true}, // 050 NEW
	{Table: "users", Column: "username", Type: "text", NotNull: true},
	{Table: "users", Column: "email", Type: "text", NotNull: true},
	{Table: "users", Column: "password_hash", Type: "text", NotNull: true},
	{Table: "users", Column: "password_reset_required", Type: "boolean", NotNull: true},
	{Table: "users", Column: "status", Type: "text", NotNull: true},
	{Table: "users", Column: "creation_source", Type: "text", NotNull: true},
	{Table: "users", Column: "authz_epoch", Type: "bigint", NotNull: true},
	{Table: "users", Column: "password_version", Type: "bigint", NotNull: true}, // S6 022
	{Table: "users", Column: "created_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "users", Column: "updated_at", Type: pgTypeTSTZ, NotNull: true},
	// Auto-lockout bookkeeping (032_users_failed_login.sql) — ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01.
	// failed_login_count carries a NOT NULL DEFAULT 0 + CHECK(>=0) (registered
	// in expectedChecks below as users_failed_login_count_positive); the two
	// timestamp columns are NULL-able (NULL == no failure yet / no TTL).
	{Table: "users", Column: "failed_login_count", Type: "integer", NotNull: true},
	{Table: "users", Column: "last_failed_at", Type: pgTypeTSTZ, NotNull: false},
	{Table: "users", Column: "locked_until", Type: pgTypeTSTZ, NotNull: false},
	// config_entries.version + feature_flags.version — PR449-F7 carry-over
	// gate columns. Original tables predate S3F structural checks; only the
	// version column is asserted here (existence + type + NOT NULL).
	{Table: "config_entries", Column: "version", Type: "integer", NotNull: true},
	{Table: "feature_flags", Column: "version", Type: "integer", NotNull: true},
	// sessions (018_sessions.sql + 026_restore_sessions_authz_epoch_at_issue.sql)
	{Table: "sessions", Column: "id", Type: "text", NotNull: true},
	{Table: "sessions", Column: "subject_id", Type: "uuid", NotNull: true},
	{Table: "sessions", Column: "jti", Type: "text", NotNull: true},
	{Table: "sessions", Column: "expires_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "sessions", Column: "created_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "sessions", Column: "revoked_at", Type: pgTypeTSTZ, NotNull: false},
	// S4d: row-level credential provenance. ADR-credential §A8 — sessionvalidate
	// compares user.authz_epoch with view.authz_epoch_at_issue, NOT JWT claim.
	// Migration 028 adds CHECK(>0) as the hard DB guarantee; schema_guard
	// asserts type/NOT NULL here and CHECK presence in expectedChecks.
	{Table: "sessions", Column: "authz_epoch_at_issue", Type: "bigint", NotNull: true},
	// refresh_tokens (007_refresh_tokens.sql + 027_add_refresh_tokens_authz_epoch_at_issue.sql)
	// Only the S4d-introduced column is registered here; the rest of the
	// refresh_tokens schema predates schema_guard's requiredColumns coverage.
	{Table: "refresh_tokens", Column: "authz_epoch_at_issue", Type: "bigint", NotNull: true},
	// roles (019_roles.sql + 050_accesscore_tenant_id.sql)
	// 050 drops+recreates the table; PK is now composite (tenant_id, id).
	{Table: "roles", Column: "tenant_id", Type: "text", NotNull: true}, // 050 NEW
	{Table: "roles", Column: "id", Type: "text", NotNull: true},
	{Table: "roles", Column: "name", Type: "text", NotNull: true},
	{Table: "roles", Column: "permissions", Type: "jsonb", NotNull: true},
	{Table: "roles", Column: "created_at", Type: pgTypeTSTZ, NotNull: true},
	// role_assignments (019_roles.sql + 050_accesscore_tenant_id.sql)
	// 050 drops+recreates the table; PK is (tenant_id, user_id, role_id).
	{Table: "role_assignments", Column: "tenant_id", Type: "text", NotNull: true}, // 050 NEW
	{Table: "role_assignments", Column: "user_id", Type: "uuid", NotNull: true},
	{Table: "role_assignments", Column: "role_id", Type: "text", NotNull: true},
	{Table: "role_assignments", Column: "granted_at", Type: pgTypeTSTZ, NotNull: true},
	// audit_entries (020_audit_ledger.sql + 043_audit_entries_v2.sql + 047_audit_entries_trace_id.sql)
	// 043 rebuilds the table (DROP+CREATE) with 5 NOT NULL columns added for
	// the 12-field canonical-JSON HMAC chain — no DEFAULT sentinels, callers
	// must supply values.
	// 047 adds trace_id (TEXT NOT NULL) for OTel correlation (#1048 Batch C);
	// NOT part of the HMAC chain (observability only).
	{Table: "audit_entries", Column: "id", Type: "uuid", NotNull: true},
	{Table: "audit_entries", Column: "namespace", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "seq_no", Type: "bigint", NotNull: true},
	{Table: "audit_entries", Column: "event_id", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "event_type", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "actor_id", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "subject_id", Type: "text", NotNull: true},      // 043 NEW
	{Table: "audit_entries", Column: "tenant_id", Type: "text", NotNull: true},       // 043 NEW
	{Table: "audit_entries", Column: "session_id", Type: "text", NotNull: true},      // 043 NEW
	{Table: "audit_entries", Column: "correlation_id", Type: "text", NotNull: true},  // 043 NEW
	{Table: "audit_entries", Column: "trace_id", Type: "text", NotNull: true},        // 047 NEW
	{Table: "audit_entries", Column: "occurred_at", Type: pgTypeTSTZ, NotNull: true}, // 043 NEW
	{Table: "audit_entries", Column: "timestamp", Type: pgTypeTSTZ, NotNull: true},
	{Table: "audit_entries", Column: "payload", Type: "bytea", NotNull: true},
	{Table: "audit_entries", Column: "prev_hash", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "hash", Type: "text", NotNull: true},
	// devices (029_devices.sql) — examples/iotdevice devicecell PG repo (B2.B).
	{Table: "devices", Column: "id", Type: "text", NotNull: true},
	{Table: "devices", Column: "name", Type: "text", NotNull: true},
	{Table: "devices", Column: "status", Type: "text", NotNull: true},
	{Table: "devices", Column: "last_seen", Type: pgTypeTSTZ, NotNull: true},
	// commands (030_commands.sql) — kernel/command.Queue PG adapter (B2.B).
	{Table: "commands", Column: "id", Type: "text", NotNull: true},
	{Table: "commands", Column: "device_id", Type: "text", NotNull: true},
	{Table: "commands", Column: "command_type", Type: "text", NotNull: true},
	{Table: "commands", Column: "payload", Type: "bytea", NotNull: true},
	{Table: "commands", Column: "metadata", Type: "jsonb", NotNull: true},
	{Table: "commands", Column: "status", Type: "smallint", NotNull: true},
	{Table: "commands", Column: "attempt", Type: "integer", NotNull: true},
	{Table: "commands", Column: "created_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "commands", Column: "sent_at", Type: pgTypeTSTZ, NotNull: false},
	{Table: "commands", Column: "delivered_at", Type: pgTypeTSTZ, NotNull: false},
	{Table: "commands", Column: "completed_at", Type: pgTypeTSTZ, NotNull: false},
	{Table: "commands", Column: "lease_expiry", Type: pgTypeTSTZ, NotNull: false},
	{Table: "commands", Column: "timeouts_schedule_to_send_ns", Type: "bigint", NotNull: false},
	{Table: "commands", Column: "timeouts_send_to_complete_ns", Type: "bigint", NotNull: false},
	{Table: "commands", Column: "timeouts_overall_ns", Type: "bigint", NotNull: false},
	// saga_instances (040_create_saga_tables.sql) — saga coordinator instance projection.
	{Table: "saga_instances", Column: "id", Type: "text", NotNull: true},
	{Table: "saga_instances", Column: "definition_id", Type: "text", NotNull: true},
	{Table: "saga_instances", Column: "status", Type: "smallint", NotNull: true},
	{Table: "saga_instances", Column: "current_version", Type: "bigint", NotNull: true},
	{Table: "saga_instances", Column: "lease_id", Type: "text", NotNull: false},
	{Table: "saga_instances", Column: "lease_expires_at", Type: pgTypeTSTZ, NotNull: false},
	{Table: "saga_instances", Column: "started_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "saga_instances", Column: "updated_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "saga_instances", Column: "payload_state", Type: "bytea", NotNull: false},
	// saga_events (040_create_saga_tables.sql) — append-only saga event log.
	{Table: "saga_events", Column: "instance_id", Type: "text", NotNull: true},
	{Table: "saga_events", Column: "version", Type: "bigint", NotNull: true},
	{Table: "saga_events", Column: "kind", Type: "smallint", NotNull: true},
	{Table: "saga_events", Column: "step_name", Type: "text", NotNull: false},
	{Table: "saga_events", Column: "payload", Type: "bytea", NotNull: false},
	{Table: "saga_events", Column: "created_at", Type: pgTypeTSTZ, NotNull: true},
	// projection_checkpoints (045_create_projection_checkpoints.sql) — CQRS projection
	// harness consumed-offset store. owner is reserved for v1.1 multi-pod claim and is
	// NOT written by the v1 adapter (PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01).
	{Table: "projection_checkpoints", Column: "cell_id", Type: "text", NotNull: true},
	{Table: "projection_checkpoints", Column: "projection_id", Type: "text", NotNull: true},
	{Table: "projection_checkpoints", Column: "offset_seq", Type: "bigint", NotNull: true},
	{Table: "projection_checkpoints", Column: "owner", Type: "text", NotNull: true},
	{Table: "projection_checkpoints", Column: "updated_at", Type: pgTypeTSTZ, NotNull: true},
	// reconcile_leases (046_create_reconcile_leases.sql) — kernel/reconcile
	// LeaderElector PG backend. epoch is the monotonic fencing token; expires_at is
	// the row-TTL lease authority (PR-A6 review C2).
	{Table: "reconcile_leases", Column: "reconciler_id", Type: "text", NotNull: true},
	{Table: "reconcile_leases", Column: "holder_id", Type: "text", NotNull: true},
	{Table: "reconcile_leases", Column: "epoch", Type: "bigint", NotNull: true},
	{Table: "reconcile_leases", Column: "acquired_at", Type: pgTypeTSTZ, NotNull: true},
	{Table: "reconcile_leases", Column: "expires_at", Type: pgTypeTSTZ, NotNull: true},
}

// forbiddenColumns are legacy columns that must NOT exist after migration.
var forbiddenColumns = []requiredColumn{
	// ADR-credential D1: plaintext token storage is forbidden.
	{table: "sessions", column: "access_token"},
	// S4d (PR S4d) restored sessions.authz_epoch_at_issue via migration 026.
	// ADR §0 A1 (the original "drop" justification) is RETRACTED — the row is
	// credential provenance source-of-truth, not a JWT claim mirror. See ADR §A8.
}

// expectedPKs is the primary key registry.
var expectedPKs = []expectedPK{
	{Table: "users", Columns: []string{"id"}},
	{Table: "sessions", Columns: []string{"id"}},
	// 050: roles PK is now composite (tenant_id, id) — roles are per-tenant scoped.
	{Table: "roles", Columns: []string{"tenant_id", "id"}},
	// 050: role_assignments PK is now (tenant_id, user_id, role_id).
	{Table: "role_assignments", Columns: []string{"tenant_id", "user_id", "role_id"}},
	// audit_entries (020_audit_ledger.sql + 043_audit_entries_v2.sql rebuild)
	{Table: "audit_entries", Columns: []string{"id"}},
	// devices / commands (029, 030) — B2.B.
	{Table: "devices", Columns: []string{"id"}},
	{Table: "commands", Columns: []string{"id"}},
	// saga_instances: PK on id (040_create_saga_tables.sql).
	{Table: "saga_instances", Columns: []string{"id"}},
	// saga_events: composite PK (instance_id, version) (040_create_saga_tables.sql).
	{Table: "saga_events", Columns: []string{"instance_id", "version"}},
	// projection_checkpoints: composite PK (cell_id, projection_id) (045_create_projection_checkpoints.sql).
	{Table: "projection_checkpoints", Columns: []string{"cell_id", "projection_id"}},
	// reconcile_leases: PK on reconciler_id (046_create_reconcile_leases.sql).
	{Table: "reconcile_leases", Columns: []string{"reconciler_id"}},
}

// expectedDefaults is the load-bearing column-default registry. Only defaults a
// write path relies upon by omitting the column are registered here.
var expectedDefaults = []expectedDefault{
	// projection_checkpoints.owner (045) — the v1 upsert OMITS owner (forbidden by
	// PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01), so the column's NOT NULL
	// constraint is only satisfiable via this DEFAULT ''. A dropped/changed default
	// would make the first SaveOffset fail at write time; asserting it here surfaces
	// the drift at startup (readyz) instead.
	{Table: "projection_checkpoints", Column: "owner", Default: "''::text"},
	// users.password_version (022 → 050 rebuild) — insertUserSQL omits this column
	// and relies on DEFAULT 0 to satisfy the NOT NULL constraint (migration 033
	// adds users_password_version_non_negative CHECK >= 0). A dropped default would
	// cause every new-user Create to fail at write time.
	{Table: "users", Column: "password_version", Default: "0"},
}

// expectedIndexes covers both unique and non-unique indexes across S3F tables.
// Columns is the DDL key-column order sourced from the migration SQL file
// (not from DB catalog output — that would be tautological).
// Partial index WHERE predicates are NOT listed in Columns (predicate columns
// are not key columns). Expression indexes use "(expr)" as a sentinel for any
// expression-valued key position.
var expectedIndexes = []expectedIndex{
	// outbox_entries — projection stream position (049_outbox_entries_seq.sql / #1368).
	// The relay claim index (idx_outbox_pending*) is intentionally not registered
	// here (it evolves with the relay state machine, independent of this guard);
	// idx_outbox_seq is tracked because the projection ReplaySource/Cursor depend
	// on it for ordered range scans, so a partial migration must fail fast.
	{Table: "outbox_entries", Name: "idx_outbox_seq", Unique: true, Columns: []string{"seq"}},
	// users (017_users.sql)
	// 050: idx_users_username and idx_users_email are now composite
	// UNIQUE(tenant_id, username) / UNIQUE(tenant_id, email) — same names, still unique.
	{Table: "users", Name: "idx_users_username", Unique: true, Columns: []string{"tenant_id", "username"}},
	{Table: "users", Name: "idx_users_email", Unique: true, Columns: []string{"tenant_id", "email"}},
	{Table: "users", Name: "idx_users_status", Unique: false, Columns: []string{"status"}},
	// 050: support index for role_assignments FK (tenant_id, user_id) reference.
	{Table: "users", Name: "idx_users_tenant_id_id", Unique: true, Columns: []string{"tenant_id", "id"}},
	// sessions (018_sessions.sql)
	{Table: "sessions", Name: "idx_sessions_jti", Unique: true, Columns: []string{"jti"}},
	// partial index: WHERE revoked_at IS NULL — key column only
	{Table: "sessions", Name: "idx_sessions_subject_active", Unique: false, Columns: []string{"subject_id"}},
	{Table: "sessions", Name: "idx_sessions_expires", Unique: false, Columns: []string{"expires_at"}},
	// roles: no additional non-PK indexes in migration 019
	// role_assignments (049_accesscore_tenant_id.sql) — composite (tenant_id, role_id)
	{Table: "role_assignments", Name: "idx_role_assignments_role", Unique: false, Columns: []string{"tenant_id", "role_id"}},
	// audit_entries (020_audit_ledger.sql + 021 event_id unique;
	// 043_audit_entries_v2.sql rebuilds the table preserving index names;
	// 048 adds idx_audit_namespace_trace_id CONCURRENTLY for TraceID filter)
	// uq_audit_namespace_seq is a UNIQUE constraint (inline DDL) — PG creates
	// an index for it; key columns mirror CONSTRAINT ... UNIQUE (namespace, seq_no).
	{Table: "audit_entries", Name: "uq_audit_namespace_seq", Unique: true, Columns: []string{"namespace", "seq_no"}},
	// idx_audit_namespace_ts_id: (namespace, timestamp DESC, id ASC)
	{Table: "audit_entries", Name: "idx_audit_namespace_ts_id", Unique: false, Columns: []string{"namespace", "timestamp", "id"}},
	{Table: "audit_entries", Name: "idx_audit_namespace_event_type", Unique: false, Columns: []string{"namespace", "event_type"}},
	{Table: "audit_entries", Name: "uq_audit_namespace_event_id", Unique: true, Columns: []string{"namespace", "event_id"}},
	// 048_audit_entries_trace_id_index.sql: (namespace, trace_id) — leading column matters for filter pushdown
	{Table: "audit_entries", Name: "idx_audit_namespace_trace_id", Unique: false, Columns: []string{"namespace", "trace_id"}},
	// 051_audit_entries_tenant_index.sql: tenant-aware keyset pagination (namespace, tenant_id, timestamp DESC, id ASC)
	// supports auditquery tenant-scoped reads (PR-2a AuditFilters.TenantID predicate).
	{
		Table: "audit_entries", Name: "idx_audit_namespace_tenant_ts_id",
		Unique: false, Columns: []string{"namespace", "tenant_id", "timestamp", "id"},
	},
	// devices / commands (029, 030, 031) — B2.B.
	{Table: "devices", Name: "idx_devices_status", Unique: false, Columns: []string{"status"}},
	// 030_commands.sql partial indexes — Columns lists only key columns, not WHERE predicate columns
	{Table: "commands", Name: "idx_commands_pending_fifo", Unique: false, Columns: []string{"device_id", "created_at"}},
	{Table: "commands", Name: "idx_commands_active_lease", Unique: false, Columns: []string{"lease_expiry"}},
	{Table: "commands", Name: "idx_commands_device_active", Unique: false, Columns: []string{"device_id", "status", "created_at"}},
	// 031_commands_idempotency_unique.sql: expression index on (metadata->>'_idempotency_key')
	// The key position is an expression; pg_index.indkey = 0 for expression columns,
	// pg_attribute.attname is NULL. The sentinel "(expr)" marks this position.
	{Table: "commands", Name: "idx_commands_idempotency_key", Unique: true, Columns: []string{"(expr)"}},
	// saga_instances (040_create_saga_tables.sql) — partial index over claimable rows.
	{Table: "saga_instances", Name: "idx_saga_instances_claimable", Unique: false, Columns: []string{"started_at", "id"}},
}

// expectedFKs is the foreign key constraint registry. ON DELETE action uses
// single-char PG catalog codes from pg_constraint.confdeltype:
//
//	'a' = NO ACTION (default; no clause in DDL)
//	'r' = RESTRICT
//	'c' = CASCADE
//	'n' = SET NULL
//	'd' = SET DEFAULT
//
// ref: https://www.postgresql.org/docs/current/catalog-pg-constraint.html
var expectedFKs = []expectedFK{
	{
		Table:      "sessions",
		Constraint: "sessions_subject_id_fkey",
		Columns:    []string{"subject_id"},
		RefTable:   "users",
		RefColumns: []string{"id"},
		OnDelete:   "c", // CASCADE — migrations/018_sessions.sql
	},
	{
		// 050: (tenant_id, user_id) references users(tenant_id, id) via UNIQUE(tenant_id, id)
		// support index. This enforces same-tenant user membership at the DB layer,
		// preventing cross-tenant authorization grants. The (tenant_id, user_id)
		// local column ORDER is the isolation pair — a swap must be rejected (F6).
		// ON DELETE CASCADE: removing a user removes all their role_assignments.
		Table:      "role_assignments",
		Constraint: "role_assignments_user_id_fkey",
		Columns:    []string{"tenant_id", "user_id"},
		RefTable:   "users",
		RefColumns: []string{"tenant_id", "id"},
		OnDelete:   "c", // CASCADE — migrations/049_accesscore_tenant_id.sql
	},
	{
		// 050: (tenant_id, role_id) references roles composite PK (tenant_id, id).
		// ON DELETE RESTRICT: cannot delete a role that has active assignments.
		Table:      "role_assignments",
		Constraint: "role_assignments_role_id_fkey",
		Columns:    []string{"tenant_id", "role_id"},
		RefTable:   "roles",
		RefColumns: []string{"tenant_id", "id"},
		OnDelete:   "r", // RESTRICT — migrations/049_accesscore_tenant_id.sql
	},
	{
		Table:      "commands",
		Constraint: "commands_device_id_fkey",
		Columns:    []string{"device_id"},
		RefTable:   "devices",
		RefColumns: []string{"id"},
		OnDelete:   "r", // RESTRICT — migrations/030_commands.sql (B2.B)
	},
	// saga_events → saga_instances ON DELETE CASCADE (040_create_saga_tables.sql).
	{
		Table:      "saga_events",
		Constraint: "saga_events_instance_id_fkey",
		Columns:    []string{"instance_id"},
		RefTable:   "saga_instances",
		RefColumns: []string{"id"},
		OnDelete:   "c", // CASCADE — migration 040
	},
}

// expectedTriggers is the trigger registry.
//
// Migration 024 (S4.0) replaced the migration-019 `last_admin_protected`
// trigger on role_assignments with two triggers sharing
// effective_admin_invariant_fn: one on role_assignments (direct DELETE
// bypass safety net) and one on users (BEFORE UPDATE OR DELETE) to catch
// status transitions that previously bypassed the role_assignments-only
// trigger. Both names and the shared function are required-present.
var expectedTriggers = []expectedTrigger{
	{
		Table:    "role_assignments",
		Name:     "effective_admin_invariant_on_role_assignments",
		Function: "effective_admin_invariant_fn",
		Enabled:  true,
	},
	{
		Table:    "users",
		Name:     "effective_admin_invariant_on_users",
		Function: "effective_admin_invariant_fn",
		Enabled:  true,
	},
}

// expectedFunctions is the PL/pgSQL function registry.
var expectedFunctions = []expectedFunction{
	{Name: "effective_admin_invariant_fn"},
}

// expectedChecks is the CHECK constraint registry.
// users_status_chk and users_creation_source_chk are added by migration 023.
// users_authz_epoch_positive, sessions_authz_epoch_at_issue_positive, and
// refresh_tokens_authz_epoch_at_issue_positive are added by migration 028
// (S4d P2.a — authz_epoch > 0 hard DB invariant).
// users_password_version_non_negative is added by migration 033
// (#940 P2-1 — password_version >= 0 defense-in-depth).
var expectedChecks = []expectedCheck{
	{Table: "users", Name: "users_status_chk"},
	{Table: "users", Name: "users_creation_source_chk"},
	{Table: "users", Name: "users_authz_epoch_positive"},
	{Table: "users", Name: "users_password_version_non_negative"},
	// Auto-lockout counter non-negativity (032_users_failed_login.sql) —
	// ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01. Named explicitly in the
	// migration via CONSTRAINT users_failed_login_count_positive so the
	// guard can pin it independently of PG's auto-naming convention.
	{Table: "users", Name: "users_failed_login_count_positive"},
	{Table: "sessions", Name: "sessions_authz_epoch_at_issue_positive"},
	{Table: "refresh_tokens", Name: "refresh_tokens_authz_epoch_at_issue_positive"},
	// devices / commands (029, 030) — B2.B.
	{Table: "devices", Name: "devices_status_chk"},
	{Table: "commands", Name: "commands_status_chk"},
	{Table: "commands", Name: "commands_attempt_chk"},
	// audit_entries hash-format guard (020_audit_ledger.sql + 043_audit_entries_v2.sql
	// rebuild preserves the constraint name) — seq_no-coupled 64-char lowercase
	// hex format for prev_hash/hash with the genesis exception.
	{Table: "audit_entries", Name: "ck_audit_hash_format"},
	// saga_instances (040_create_saga_tables.sql).
	{Table: "saga_instances", Name: "saga_instances_status_range"},
	{Table: "saga_instances", Name: "saga_instances_version_nonneg"},
	{Table: "saga_instances", Name: "saga_instances_lease_paired"},
	// saga_events (040_create_saga_tables.sql).
	{Table: "saga_events", Name: "saga_events_kind_range"},
	{Table: "saga_events", Name: "saga_events_version_positive"},
}

// ---------------------------------------------------------------------------
// Dimension helper: columns (type + nullability)
// ---------------------------------------------------------------------------

// verifyColumns checks each entry in expectedColumns against pg_attribute.
func verifyColumns(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT format_type(a.atttypid, a.atttypmod), a.attnotnull, a.attidentity::text
	  FROM pg_attribute a
	  JOIN pg_class c ON c.oid = a.attrelid
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	 WHERE n.nspname = current_schema()
	   AND c.relname = $1
	   AND a.attname = $2
	   AND a.attnum > 0
	   AND NOT a.attisdropped`

	for _, ec := range expectedColumns {
		var gotType string
		var gotNotNull bool
		var gotIdentity string // pg_attribute.attidentity: '' none, 'a' ALWAYS, 'd' BY DEFAULT
		err := pool.inner.QueryRow(ctx, q, ec.Table, ec.Column).Scan(&gotType, &gotNotNull, &gotIdentity)
		if err != nil {
			// No row means column is missing.
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: required column missing",
				errcode.WithDetails(
					errcode.PublicString("dimension", "column"),
					errcode.PublicString("table", ec.Table),
					errcode.PublicString("column", ec.Column),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(queryErrFmt, err))),
			)
		}
		if gotType != ec.Type {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: column type mismatch",
				errcode.WithDetails(
					errcode.PublicString("dimension", "column_type"),
					errcode.PublicString("table", ec.Table),
					errcode.PublicString("column", ec.Column),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got %q want %q", gotType, ec.Type))),
			)
		}
		if gotNotNull != ec.NotNull {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: column nullability mismatch",
				errcode.WithDetails(
					errcode.PublicString("dimension", "column_nullability"),
					errcode.PublicString("table", ec.Table),
					errcode.PublicString("column", ec.Column),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got not_null=%v want %v", gotNotNull, ec.NotNull))),
			)
		}
		if ec.Identity && gotIdentity != "a" {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: column must be GENERATED ALWAYS AS IDENTITY",
				errcode.WithDetails(
					errcode.PublicString("dimension", "column_identity"),
					errcode.PublicString("table", ec.Table),
					errcode.PublicString("column", ec.Column),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got attidentity=%q want \"a\" (ALWAYS)", gotIdentity))),
			)
		}
	}
	return nil
}

// verifyDefaults checks each entry in expectedDefaults against the column's
// actual DEFAULT expression (pg_get_expr). The LEFT JOIN + COALESCE renders a
// missing default as the empty string, which never equals a registered
// non-empty default expression — so a dropped default is reported as a
// mismatch. verifyColumns runs first in VerifyExpectedShape, so a registered
// column is guaranteed to exist here (a genuine missing column fails earlier).
func verifyDefaults(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
	  FROM pg_attribute a
	  JOIN pg_class c ON c.oid = a.attrelid
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	 WHERE n.nspname = current_schema()
	   AND c.relname = $1
	   AND a.attname = $2
	   AND a.attnum > 0
	   AND NOT a.attisdropped`

	for _, ed := range expectedDefaults {
		var gotDefault string
		if err := pool.inner.QueryRow(ctx, q, ed.Table, ed.Column).Scan(&gotDefault); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: query column default", err)
		}
		if gotDefault != ed.Default {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: column default mismatch",
				errcode.WithDetails(
					errcode.PublicString("dimension", "column_default"),
					errcode.PublicString("table", ed.Table),
					errcode.PublicString("column", ed.Column),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got %q want %q", gotDefault, ed.Default))),
			)
		}
	}
	return nil
}

// verifyForbiddenColumns checks that legacy columns do NOT exist.
func verifyForbiddenColumns(ctx context.Context, pool *Pool) error {
	for _, r := range forbiddenColumns {
		ok, err := columnExists(ctx, pool, r.table, r.column)
		if err != nil {
			return err
		}
		if ok {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: forbidden legacy column present (partial migration)",
				errcode.WithDetails(
					errcode.PublicString("dimension", "forbidden_column"),
					errcode.PublicString("table", r.table),
					errcode.PublicString("column", r.column),
				),
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: primary keys
// ---------------------------------------------------------------------------

// verifyPrimaryKeys checks that each table's PRIMARY KEY matches the registry,
// including column order. The query resolves conkey (array of column attnum) to
// column names and returns them in PK ordinal order via ORDER BY
// array_position(conkey, attnum). Comparison uses slices.Equal (ordered) so
// that PK column order drift is detected — e.g., PRIMARY KEY (a, b) vs
// PRIMARY KEY (b, a) produces different physical index pages and query plans.
func verifyPrimaryKeys(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT a.attname
	  FROM pg_constraint co
	  JOIN pg_class c ON c.oid = co.conrelid
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	  JOIN pg_attribute a ON a.attrelid = c.oid
	   AND a.attnum = ANY(co.conkey)
	 WHERE n.nspname = current_schema()
	   AND c.relname = $1
	   AND co.contype = 'p'
	 ORDER BY array_position(co.conkey, a.attnum)`

	for _, pk := range expectedPKs {
		rows, err := pool.inner.Query(ctx, q, pk.Table)
		if err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: query primary key", err)
		}
		var got []string
		for rows.Next() {
			var col string
			if scanErr := rows.Scan(&col); scanErr != nil {
				rows.Close()
				return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
					"schema_guard: scan primary key column", scanErr)
			}
			got = append(got, col)
		}
		rows.Close()
		if rows.Err() != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: iterate primary key columns", rows.Err())
		}
		if len(got) == 0 {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: primary key missing",
				errcode.WithDetails(
					errcode.PublicString("dimension", "primary_key"),
					errcode.PublicString("table", pk.Table),
				),
			)
		}
		if !slices.Equal(got, pk.Columns) {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: primary key column mismatch",
				errcode.WithDetails(
					errcode.PublicString("dimension", "primary_key"),
					errcode.PublicString("table", pk.Table),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got %v want %v", got, pk.Columns))),
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: indexes (unique + non-unique)
// ---------------------------------------------------------------------------

// verifyIndexes checks unique/non-unique index presence, uniqueness flag, and
// key-column order for every entry in expectedIndexes.
func verifyIndexes(ctx context.Context, pool *Pool) error {
	// Query returns indisunique and the ordered key-column names.
	// unnest(i.indkey) WITH ORDINALITY expands the key-column attnum array;
	// LEFT JOIN pg_attribute maps attnum → attname (NULL for expression columns).
	// Only key columns are returned: ord <= i.indnkeyatts excludes any INCLUDE
	// columns that may appear at the end of indkey.
	// Expression columns have indkey[n] = 0 and attname IS NULL; COALESCE maps
	// them to the sentinel "(expr)".
	const q = `
	SELECT i.indisunique,
	       array_agg(
	           COALESCE(a.attname, '(expr)')
	           ORDER BY ord
	       ) AS key_columns
	  FROM pg_index i
	  JOIN pg_class ci ON ci.oid = i.indexrelid
	  JOIN pg_class ct ON ct.oid = i.indrelid
	  JOIN pg_namespace n ON n.oid = ct.relnamespace
	  JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS u(attnum, ord)
	            ON u.ord <= i.indnkeyatts
	  LEFT JOIN pg_attribute a
	            ON a.attrelid = i.indrelid AND a.attnum = u.attnum AND a.attnum > 0
	 WHERE n.nspname = current_schema()
	   AND ct.relname = $1
	   AND ci.relname = $2
	   AND NOT i.indisprimary
	 GROUP BY i.indisunique, i.indnkeyatts`

	for _, idx := range expectedIndexes {
		if err := verifyOneIndex(ctx, pool, idx, q); err != nil {
			return err
		}
	}
	return nil
}

// verifyOneIndex verifies a single expectedIndex entry against the pg catalog.
// Extracted to keep verifyIndexes below the cognitive complexity limit.
func verifyOneIndex(ctx context.Context, pool *Pool, idx expectedIndex, q string) error {
	var gotUnique bool
	var gotColumns []string
	err := pool.inner.QueryRow(ctx, q, idx.Table, idx.Name).Scan(&gotUnique, &gotColumns)
	if err != nil {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: expected index missing",
			errcode.WithDetails(
				errcode.PublicString("dimension", "index"),
				errcode.PublicString("table", idx.Table),
				errcode.PublicString("index", idx.Name),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(queryErrFmt, err))),
		)
	}
	if gotUnique != idx.Unique {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: index uniqueness mismatch",
			errcode.WithDetails(
				errcode.PublicString("dimension", "index_unique"),
				errcode.PublicString("table", idx.Table),
				errcode.PublicString("index", idx.Name),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got unique=%v want %v", gotUnique, idx.Unique))),
		)
	}
	if !slices.Equal(gotColumns, idx.Columns) {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: index columns mismatch",
			errcode.WithDetails(
				errcode.PublicString("dimension", "index_columns"),
				errcode.PublicString("table", idx.Table),
				errcode.PublicString("index", idx.Name),
			),
			errcode.WithInternal(
				errcode.InternalAttr("got", strings.Join(gotColumns, ",")),
				errcode.InternalAttr("want", strings.Join(idx.Columns, ",")),
			),
		)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: foreign keys
// ---------------------------------------------------------------------------

// verifyForeignKeys checks FK constraints including ON DELETE action.
func verifyForeignKeys(ctx context.Context, pool *Pool) error {
	// confdeltype is PG `char` (single-byte: 'a' NO ACTION / 'r' RESTRICT /
	// 'c' CASCADE / 'n' SET NULL / 'd' SET DEFAULT). Cast to text so pgx's
	// binary protocol can scan into *string (default binary scan of `char`
	// OID 18 into *string is rejected).
	const fkQ = `
	SELECT co.oid, ref.relname, co.confdeltype::text
	  FROM pg_constraint co
	  JOIN pg_class c ON c.oid = co.conrelid
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	  JOIN pg_class ref ON ref.oid = co.confrelid
	 WHERE n.nspname = current_schema()
	   AND c.relname = $1
	   AND co.conname = $2
	   AND co.contype = 'f'`

	for _, fk := range expectedFKs {
		if err := verifyOneForeignKey(ctx, pool, fk, fkQ); err != nil {
			return err
		}
	}
	return nil
}

// verifyOneForeignKey checks a single FK entry against the pg catalog.
// Extracted to keep verifyForeignKeys below the cognitive complexity limit.
func verifyOneForeignKey(ctx context.Context, pool *Pool, fk expectedFK, fkQ string) error {
	const refColsQ = `
	SELECT a.attname
	  FROM pg_constraint co
	  JOIN pg_attribute a ON a.attrelid = co.confrelid
	   AND a.attnum = ANY(co.confkey)
	 WHERE co.oid = $1
	 ORDER BY array_position(co.confkey, a.attnum)`

	var oid uint32
	var gotRefTable, gotOnDelete string
	err := pool.inner.QueryRow(ctx, fkQ, fk.Table, fk.Constraint).Scan(&oid, &gotRefTable, &gotOnDelete)
	if err != nil {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: expected foreign key missing",
			errcode.WithDetails(
				errcode.PublicString("dimension", "foreign_key"),
				errcode.PublicString("table", fk.Table),
				errcode.PublicString("constraint", fk.Constraint),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(queryErrFmt, err))),
		)
	}
	if gotRefTable != fk.RefTable {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: foreign key references wrong table",
			errcode.WithDetails(
				errcode.PublicString("dimension", "foreign_key"),
				errcode.PublicString("table", fk.Table),
				errcode.PublicString("constraint", fk.Constraint),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got ref_table=%q want %q", gotRefTable, fk.RefTable))),
		)
	}
	if gotOnDelete != fk.OnDelete {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: foreign key on-delete action mismatch",
			errcode.WithDetails(
				errcode.PublicString("dimension", "foreign_key"),
				errcode.PublicString("table", fk.Table),
				errcode.PublicString("constraint", fk.Constraint),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got on_delete=%q want %q", gotOnDelete, fk.OnDelete))),
		)
	}
	// localColsQ resolves the FK's LOCAL constrained columns (conkey) on the
	// constrained table; refColsQ (above) resolves the REFERENCED columns
	// (confkey). Both ORDER BY array_position so the scan order is canonical and
	// slices.Equal enforces order (F6: a composite tenant FK that swaps the
	// (tenant_id, col) pair is a distinct, security-relevant drift).
	const localColsQ = `
	SELECT a.attname
	  FROM pg_constraint co
	  JOIN pg_attribute a ON a.attrelid = co.conrelid
	   AND a.attnum = ANY(co.conkey)
	 WHERE co.oid = $1
	 ORDER BY array_position(co.conkey, a.attnum)`

	if err := verifyFKColumns(ctx, pool, fk, localColsQ, oid, fk.Columns, "local"); err != nil {
		return err
	}
	return verifyFKColumns(ctx, pool, fk, refColsQ, oid, fk.RefColumns, "referenced")
}

// verifyFKColumns checks that the FK's columns (local conkey or referenced
// confkey, selected by colsQ) match want IN ORDER. colsQ MUST ORDER BY
// array_position so the scan order is canonical; slices.Equal then validates
// both membership and order. side ("local"/"referenced") tags the error.
func verifyFKColumns(ctx context.Context, pool *Pool, fk expectedFK, colsQ string, oid uint32, want []string, side string) error {
	rows, err := pool.inner.Query(ctx, colsQ, oid)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: query FK columns", err)
	}
	var got []string
	for rows.Next() {
		var col string
		if scanErr := rows.Scan(&col); scanErr != nil {
			rows.Close()
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: scan FK column", scanErr)
		}
		got = append(got, col)
	}
	rows.Close()
	if rows.Err() != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: iterate FK columns", rows.Err())
	}
	if !slices.Equal(got, want) {
		return errcode.New(
			errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: foreign key columns mismatch",
			errcode.WithDetails(
				errcode.PublicString("dimension", "foreign_key"),
				errcode.PublicString("table", fk.Table),
				errcode.PublicString("constraint", fk.Constraint),
				errcode.PublicString("side", side),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("%s columns: got %v want %v", side, got, want))),
		)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: triggers
// ---------------------------------------------------------------------------

// verifyTriggers checks trigger presence, enabled state, and function name.
func verifyTriggers(ctx context.Context, pool *Pool) error {
	// pg_trigger.tgenabled is `char` (single-byte: 'O' origin / 'D' disabled /
	// 'R' replica / 'A' always). Cast to text so pgx binary protocol can scan
	// into *string — same constraint as confdeltype above (PG char OID 18 has
	// no default binary→string codec).
	const q = `
	SELECT tg.tgenabled::text, p.proname
	  FROM pg_trigger tg
	  JOIN pg_class c ON c.oid = tg.tgrelid
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	  JOIN pg_proc p ON p.oid = tg.tgfoid
	 WHERE n.nspname = current_schema()
	   AND c.relname = $1
	   AND tg.tgname = $2
	   AND NOT tg.tgisinternal`

	for _, tr := range expectedTriggers {
		var gotEnabled string
		var gotFn string
		err := pool.inner.QueryRow(ctx, q, tr.Table, tr.Name).Scan(&gotEnabled, &gotFn)
		if err != nil {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected trigger missing",
				errcode.WithDetails(
					errcode.PublicString("dimension", "trigger"),
					errcode.PublicString("table", tr.Table),
					errcode.PublicString("trigger", tr.Name),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(queryErrFmt, err))),
			)
		}
		isEnabled := gotEnabled == "O"
		if isEnabled != tr.Enabled {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: trigger enabled state mismatch",
				errcode.WithDetails(
					errcode.PublicString("dimension", "trigger_enabled"),
					errcode.PublicString("table", tr.Table),
					errcode.PublicString("trigger", tr.Name),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
					"tgenabled=%q (enabled=%v) want enabled=%v",
					gotEnabled, isEnabled, tr.Enabled,
				))),
			)
		}
		if gotFn != tr.Function {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: trigger function mismatch",
				errcode.WithDetails(
					errcode.PublicString("dimension", "trigger_function"),
					errcode.PublicString("table", tr.Table),
					errcode.PublicString("trigger", tr.Name),
				),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got fn=%q want %q", gotFn, tr.Function))),
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: PL/pgSQL functions
// ---------------------------------------------------------------------------

// verifyFunctions checks that each expected PL/pgSQL function exists.
func verifyFunctions(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT EXISTS (
	  SELECT 1
	    FROM pg_proc p
	    JOIN pg_namespace n ON n.oid = p.pronamespace
	   WHERE n.nspname = current_schema()
	     AND p.proname = $1
	)`

	for _, fn := range expectedFunctions {
		var exists bool
		if err := pool.inner.QueryRow(ctx, q, fn.Name).Scan(&exists); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: query function existence", err)
		}
		if !exists {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected function missing",
				errcode.WithDetails(
					errcode.PublicString("dimension", "function"),
					errcode.PublicString("function", fn.Name),
				),
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: CHECK constraints
// ---------------------------------------------------------------------------

// verifyChecks checks that each expected CHECK constraint exists on its table.
func verifyChecks(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT EXISTS (
	  SELECT 1
	    FROM pg_constraint co
	    JOIN pg_class c ON c.oid = co.conrelid
	    JOIN pg_namespace n ON n.oid = c.relnamespace
	   WHERE n.nspname = current_schema()
	     AND c.relname = $1
	     AND co.conname = $2
	     AND co.contype = 'c'
	)`

	for _, chk := range expectedChecks {
		var exists bool
		if err := pool.inner.QueryRow(ctx, q, chk.Table, chk.Name).Scan(&exists); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: query check constraint", err)
		}
		if !exists {
			return errcode.New(
				errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected check constraint missing",
				errcode.WithDetails(
					errcode.PublicString("dimension", "check"),
					errcode.PublicString("table", chk.Table),
					errcode.PublicString("constraint", chk.Name),
				),
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// columnExists is the predicate behind verifyForbiddenColumns. Scoped to
// current_schema() so test-schema parallelism does not produce false
// positives.
func columnExists(ctx context.Context, pool *Pool, table, column string) (bool, error) {
	const q = `SELECT EXISTS (
		SELECT 1
		  FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND table_name = $1
		   AND column_name = $2
	)`
	var exists bool
	if err := pool.inner.QueryRow(ctx, q, table, column).Scan(&exists); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: probe column", err)
	}
	return exists, nil
}

// VerifyNoInvalidIndexes is the fail-fast counterpart of DetectInvalidIndexes
// for the cmd/corebundle startup path. It returns ErrAdapterPGInvalidIndex
// when any pg_index row has indisvalid=false, replacing the prior
// warn-continue defense (B2-X-03). Operators must DROP the invalid
// index manually before the binary will start.
//
// Use DetectInvalidIndexes when you need the index list for diagnostics
// (e.g. /readyz?verbose response). Use VerifyNoInvalidIndexes when you want
// startup to abort.
func VerifyNoInvalidIndexes(ctx context.Context, pool *Pool) error {
	indexes, err := DetectInvalidIndexes(ctx, pool)
	if err != nil {
		return err
	}
	if len(indexes) == 0 {
		return nil
	}
	names := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		names = append(names, idx.Index)
	}
	return errcode.New(errcode.KindInternal, ErrAdapterPGInvalidIndex,
		"schema_guard: invalid indexes present at startup",
		errcode.WithDetails(errcode.PublicInt("count", len(indexes))),
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("invalid indexes: %s", strings.Join(names, ", ")))))
}

// DetectInvalidIndexes queries pg_index for any indexes marked as invalid
// (indisvalid = false) within the current schema. These can occur when
// CREATE INDEX CONCURRENTLY is interrupted (orphan invalid index). The caller
// should log a warning and consider manual cleanup (DROP INDEX).
//
// Rollout safety: a peer pod or a manual operator running CREATE INDEX
// CONCURRENTLY on a live table will momentarily produce a row in
// pg_stat_progress_create_index while indisvalid=false. We LEFT JOIN that
// view and exclude any index that currently has an active build session so
// that a normal rolling deploy does not block startup. Only fully orphaned
// invalid indexes (no active builder) are reported.
//
// pg_stat_progress_create_index is available in PostgreSQL 12+. GoCell
// requires PostgreSQL 14+, so this join is always safe.
//
// The check is scoped to current_schema() so that in-progress CONCURRENTLY
// builds in other schemas (e.g. parallel test schemas) do not block
// migrations in unrelated schemas. The returned Index/Table fields are
// schema-qualified ("public.idx_foo") so multi-schema deployments do not
// observe spurious matches across schemas with reused names.
//
// Returns an empty slice when no invalid indexes are found.
func DetectInvalidIndexes(ctx context.Context, pool *Pool) ([]InvalidIndex, error) {
	const q = `SELECT n.nspname || '.' || c.relname AS index_name,
		nt.nspname || '.' || t.relname AS table_name
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_namespace nt ON nt.oid = t.relnamespace
		LEFT JOIN pg_stat_progress_create_index p ON p.index_relid = i.indexrelid
		WHERE NOT i.indisvalid
		  AND n.nspname = current_schema()
		  AND p.index_relid IS NULL`

	rows, err := pool.inner.Query(ctx, q)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: query invalid indexes", err)
	}
	defer rows.Close()

	var results []InvalidIndex
	for rows.Next() {
		var idx InvalidIndex
		if scanErr := rows.Scan(&idx.Index, &idx.Table); scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: scan invalid index", scanErr)
		}
		results = append(results, idx)
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: iterate invalid indexes", rows.Err())
	}

	return results, nil
}
