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
//   - outbox_entries     (001)  transactional outbox for event relay
//   - config_entries     (004)  cell configuration key-value store
//   - config_versions    (004)  immutable configuration version history
//   - refresh_tokens     (007)  append-only refresh token lineage
//   - feature_flags      (008)  flag definitions
//   - users              (017)  accesscore user identities
//                                 + users_status_chk, users_creation_source_chk (023 CHECK)
//                                 + effective_admin_invariant_on_users trigger (024)
//   - sessions           (018)  accesscore session / JTI store
//                                 - authz_epoch_at_issue dropped (025)
//   - roles              (019)  accesscore role definitions
//   - role_assignments   (019)  accesscore user-role grants
//                                 + effective_admin_invariant_on_role_assignments trigger (024)
//   - audit_entries      (020)  tamper-evident audit ledger (per-namespace hash chain)
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
			errcode.WithDetails(slog.Int64("db", actual), slog.Int64("binary", expected)))
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
		errcode.WithInternal(fmt.Sprintf("%d invalid index(es): %s", len(indexes), strings.Join(names, ", "))),
		errcode.WithDetails(slog.Int("invalidCount", len(indexes))))
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
}

// expectedPK describes a table's primary key column set.
type expectedPK struct {
	Table   string
	Columns []string
}

// expectedFK describes a foreign key constraint.
type expectedFK struct {
	Table      string
	Constraint string
	RefTable   string
	RefColumns []string
	OnDelete   string // e.g. "a" = CASCADE, "r" = RESTRICT
}

// expectedIndex describes a named index (unique or non-unique).
type expectedIndex struct {
	Table  string
	Name   string
	Unique bool
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

// ---------------------------------------------------------------------------
// Expected shape registries (hardcoded per ADR-credential §5.1.3)
// ---------------------------------------------------------------------------

// expectedColumns is the authoritative column-type-nullability registry for
// the S3F-owned tables (users/sessions/roles/role_assignments) and the
// auditcore-owned audit_entries table (020_audit_ledger.sql).
var expectedColumns = []expectedColumn{
	// users (017_users.sql + 022_users_password_version.sql)
	{Table: "users", Column: "id", Type: "uuid", NotNull: true},
	{Table: "users", Column: "username", Type: "text", NotNull: true},
	{Table: "users", Column: "email", Type: "text", NotNull: true},
	{Table: "users", Column: "password_hash", Type: "text", NotNull: true},
	{Table: "users", Column: "password_reset_required", Type: "boolean", NotNull: true},
	{Table: "users", Column: "status", Type: "text", NotNull: true},
	{Table: "users", Column: "creation_source", Type: "text", NotNull: true},
	{Table: "users", Column: "authz_epoch", Type: "bigint", NotNull: true},
	{Table: "users", Column: "password_version", Type: "bigint", NotNull: true}, // S6 022
	{Table: "users", Column: "created_at", Type: "timestamp with time zone", NotNull: true},
	{Table: "users", Column: "updated_at", Type: "timestamp with time zone", NotNull: true},
	// config_entries.version + feature_flags.version — PR449-F7 carry-over
	// gate columns. Original tables predate S3F structural checks; only the
	// version column is asserted here (existence + type + NOT NULL).
	{Table: "config_entries", Column: "version", Type: "integer", NotNull: true},
	{Table: "feature_flags", Column: "version", Type: "integer", NotNull: true},
	// sessions (018_sessions.sql + 025_drop_sessions_authz_epoch_at_issue.sql)
	{Table: "sessions", Column: "id", Type: "text", NotNull: true},
	{Table: "sessions", Column: "subject_id", Type: "uuid", NotNull: true},
	{Table: "sessions", Column: "jti", Type: "text", NotNull: true},
	{Table: "sessions", Column: "expires_at", Type: "timestamp with time zone", NotNull: true},
	{Table: "sessions", Column: "created_at", Type: "timestamp with time zone", NotNull: true},
	{Table: "sessions", Column: "revoked_at", Type: "timestamp with time zone", NotNull: false},
	// roles (019_roles.sql)
	{Table: "roles", Column: "id", Type: "text", NotNull: true},
	{Table: "roles", Column: "name", Type: "text", NotNull: true},
	{Table: "roles", Column: "permissions", Type: "jsonb", NotNull: true},
	{Table: "roles", Column: "created_at", Type: "timestamp with time zone", NotNull: true},
	// role_assignments (019_roles.sql)
	{Table: "role_assignments", Column: "user_id", Type: "uuid", NotNull: true},
	{Table: "role_assignments", Column: "role_id", Type: "text", NotNull: true},
	{Table: "role_assignments", Column: "granted_at", Type: "timestamp with time zone", NotNull: true},
	// audit_entries (020_audit_ledger.sql)
	{Table: "audit_entries", Column: "id", Type: "uuid", NotNull: true},
	{Table: "audit_entries", Column: "namespace", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "seq_no", Type: "bigint", NotNull: true},
	{Table: "audit_entries", Column: "event_id", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "event_type", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "actor_id", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "timestamp", Type: "timestamp with time zone", NotNull: true},
	{Table: "audit_entries", Column: "payload", Type: "bytea", NotNull: true},
	{Table: "audit_entries", Column: "prev_hash", Type: "text", NotNull: true},
	{Table: "audit_entries", Column: "hash", Type: "text", NotNull: true},
}

// forbiddenColumns are legacy columns that must NOT exist after migration.
var forbiddenColumns = []requiredColumn{
	// ADR-credential D1: plaintext token storage is forbidden.
	{table: "sessions", column: "access_token"},
	// S4b Batch 1C: authz_epoch_at_issue dropped by migration 025 — epoch is now
	// carried in the JWT claim layer, not as a per-session snapshot column.
	{table: "sessions", column: "authz_epoch_at_issue"},
}

// expectedPKs is the primary key registry.
var expectedPKs = []expectedPK{
	{Table: "users", Columns: []string{"id"}},
	{Table: "sessions", Columns: []string{"id"}},
	{Table: "roles", Columns: []string{"id"}},
	{Table: "role_assignments", Columns: []string{"user_id", "role_id"}},
	// audit_entries (020_audit_ledger.sql)
	{Table: "audit_entries", Columns: []string{"id"}},
}

// expectedIndexes covers both unique and non-unique indexes across S3F tables.
var expectedIndexes = []expectedIndex{
	// users
	{Table: "users", Name: "idx_users_username", Unique: true},
	{Table: "users", Name: "idx_users_email", Unique: true},
	{Table: "users", Name: "idx_users_status", Unique: false},
	// sessions
	{Table: "sessions", Name: "idx_sessions_jti", Unique: true},
	{Table: "sessions", Name: "idx_sessions_subject_active", Unique: false},
	{Table: "sessions", Name: "idx_sessions_expires", Unique: false},
	// roles: no additional non-PK indexes in migration 019
	// role_assignments
	{Table: "role_assignments", Name: "idx_role_assignments_role", Unique: false},
	// audit_entries (020_audit_ledger.sql)
	{Table: "audit_entries", Name: "uq_audit_namespace_seq", Unique: true},
	{Table: "audit_entries", Name: "idx_audit_namespace_ts_id", Unique: false},
	{Table: "audit_entries", Name: "idx_audit_namespace_event_type", Unique: false},
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
		RefTable:   "users",
		RefColumns: []string{"id"},
		OnDelete:   "c", // CASCADE — migrations/018_sessions.sql
	},
	{
		Table:      "role_assignments",
		Constraint: "role_assignments_user_id_fkey",
		RefTable:   "users",
		RefColumns: []string{"id"},
		OnDelete:   "c", // CASCADE — migrations/019_roles.sql
	},
	{
		Table:      "role_assignments",
		Constraint: "role_assignments_role_id_fkey",
		RefTable:   "roles",
		RefColumns: []string{"id"},
		OnDelete:   "r", // RESTRICT — migrations/019_roles.sql
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
var expectedChecks = []expectedCheck{
	{Table: "users", Name: "users_status_chk"},
	{Table: "users", Name: "users_creation_source_chk"},
}

// ---------------------------------------------------------------------------
// Dimension helper: columns (type + nullability)
// ---------------------------------------------------------------------------

// verifyColumns checks each entry in expectedColumns against pg_attribute.
func verifyColumns(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT format_type(a.atttypid, a.atttypmod), a.attnotnull
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
		err := pool.inner.QueryRow(ctx, q, ec.Table, ec.Column).Scan(&gotType, &gotNotNull)
		if err != nil {
			// No row means column is missing.
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: required column missing",
				errcode.WithDetails(
					slog.String("dimension", "column"),
					slog.String("table", ec.Table),
					slog.String("column", ec.Column),
				),
				errcode.WithInternal(fmt.Sprintf("query: %v", err)),
			)
		}
		if gotType != ec.Type {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: column type mismatch",
				errcode.WithDetails(
					slog.String("dimension", "column_type"),
					slog.String("table", ec.Table),
					slog.String("column", ec.Column),
				),
				errcode.WithInternal(fmt.Sprintf("got %q want %q", gotType, ec.Type)),
			)
		}
		if gotNotNull != ec.NotNull {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: column nullability mismatch",
				errcode.WithDetails(
					slog.String("dimension", "column_nullability"),
					slog.String("table", ec.Table),
					slog.String("column", ec.Column),
				),
				errcode.WithInternal(fmt.Sprintf("got not_null=%v want %v", gotNotNull, ec.NotNull)),
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
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: forbidden legacy column present (partial migration)",
				errcode.WithDetails(
					slog.String("dimension", "forbidden_column"),
					slog.String("table", r.table),
					slog.String("column", r.column),
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
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: primary key missing",
				errcode.WithDetails(
					slog.String("dimension", "primary_key"),
					slog.String("table", pk.Table),
				),
			)
		}
		if !slices.Equal(got, pk.Columns) {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: primary key column mismatch",
				errcode.WithDetails(
					slog.String("dimension", "primary_key"),
					slog.String("table", pk.Table),
				),
				errcode.WithInternal(fmt.Sprintf("got %v want %v", got, pk.Columns)),
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dimension helper: indexes (unique + non-unique)
// ---------------------------------------------------------------------------

// verifyIndexes checks both unique and non-unique index presence.
func verifyIndexes(ctx context.Context, pool *Pool) error {
	const q = `
	SELECT i.indisunique
	  FROM pg_index i
	  JOIN pg_class ci ON ci.oid = i.indexrelid
	  JOIN pg_class ct ON ct.oid = i.indrelid
	  JOIN pg_namespace n ON n.oid = ct.relnamespace
	 WHERE n.nspname = current_schema()
	   AND ct.relname = $1
	   AND ci.relname = $2
	   AND NOT i.indisprimary`

	for _, idx := range expectedIndexes {
		var gotUnique bool
		err := pool.inner.QueryRow(ctx, q, idx.Table, idx.Name).Scan(&gotUnique)
		if err != nil {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected index missing",
				errcode.WithDetails(
					slog.String("dimension", "index"),
					slog.String("table", idx.Table),
					slog.String("index", idx.Name),
				),
				errcode.WithInternal(fmt.Sprintf("query: %v", err)),
			)
		}
		if gotUnique != idx.Unique {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: index uniqueness mismatch",
				errcode.WithDetails(
					slog.String("dimension", "index_unique"),
					slog.String("table", idx.Table),
					slog.String("index", idx.Name),
				),
				errcode.WithInternal(fmt.Sprintf("got unique=%v want %v", gotUnique, idx.Unique)),
			)
		}
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
		return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: expected foreign key missing",
			errcode.WithDetails(
				slog.String("dimension", "foreign_key"),
				slog.String("table", fk.Table),
				slog.String("constraint", fk.Constraint),
			),
			errcode.WithInternal(fmt.Sprintf("query: %v", err)),
		)
	}
	if gotRefTable != fk.RefTable {
		return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: foreign key references wrong table",
			errcode.WithDetails(
				slog.String("dimension", "foreign_key"),
				slog.String("table", fk.Table),
				slog.String("constraint", fk.Constraint),
			),
			errcode.WithInternal(fmt.Sprintf("got ref_table=%q want %q", gotRefTable, fk.RefTable)),
		)
	}
	if gotOnDelete != fk.OnDelete {
		return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: foreign key on-delete action mismatch",
			errcode.WithDetails(
				slog.String("dimension", "foreign_key"),
				slog.String("table", fk.Table),
				slog.String("constraint", fk.Constraint),
			),
			errcode.WithInternal(fmt.Sprintf("got on_delete=%q want %q", gotOnDelete, fk.OnDelete)),
		)
	}
	return verifyFKRefColumns(ctx, pool, fk, refColsQ, oid)
}

// verifyFKRefColumns checks that the FK's referenced columns match expectations.
func verifyFKRefColumns(ctx context.Context, pool *Pool, fk expectedFK, refColsQ string, oid uint32) error {
	rows, err := pool.inner.Query(ctx, refColsQ, oid)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: query FK ref columns", err)
	}
	var gotRefCols []string
	for rows.Next() {
		var col string
		if scanErr := rows.Scan(&col); scanErr != nil {
			rows.Close()
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"schema_guard: scan FK ref column", scanErr)
		}
		gotRefCols = append(gotRefCols, col)
	}
	rows.Close()
	if rows.Err() != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"schema_guard: iterate FK ref columns", rows.Err())
	}
	if !stringSliceEqualUnordered(gotRefCols, fk.RefColumns) {
		return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
			"schema_guard: foreign key ref columns mismatch",
			errcode.WithDetails(
				slog.String("dimension", "foreign_key"),
				slog.String("table", fk.Table),
				slog.String("constraint", fk.Constraint),
			),
			errcode.WithInternal(fmt.Sprintf("got %v want %v", gotRefCols, fk.RefColumns)),
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
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected trigger missing",
				errcode.WithDetails(
					slog.String("dimension", "trigger"),
					slog.String("table", tr.Table),
					slog.String("trigger", tr.Name),
				),
				errcode.WithInternal(fmt.Sprintf("query: %v", err)),
			)
		}
		isEnabled := gotEnabled == "O"
		if isEnabled != tr.Enabled {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: trigger enabled state mismatch",
				errcode.WithDetails(
					slog.String("dimension", "trigger_enabled"),
					slog.String("table", tr.Table),
					slog.String("trigger", tr.Name),
				),
				errcode.WithInternal(fmt.Sprintf("tgenabled=%q (enabled=%v) want enabled=%v", gotEnabled, isEnabled, tr.Enabled)),
			)
		}
		if gotFn != tr.Function {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: trigger function mismatch",
				errcode.WithDetails(
					slog.String("dimension", "trigger_function"),
					slog.String("table", tr.Table),
					slog.String("trigger", tr.Name),
				),
				errcode.WithInternal(fmt.Sprintf("got fn=%q want %q", gotFn, tr.Function)),
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
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected function missing",
				errcode.WithDetails(
					slog.String("dimension", "function"),
					slog.String("function", fn.Name),
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
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: expected check constraint missing",
				errcode.WithDetails(
					slog.String("dimension", "check"),
					slog.String("table", chk.Table),
					slog.String("constraint", chk.Name),
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

// stringSliceEqualUnordered reports whether two string slices contain the same
// elements regardless of order.
func stringSliceEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// VerifyNoInvalidIndexes is the fail-fast counterpart of DetectInvalidIndexes
// for the cmd/corebundle startup path. It returns ErrAdapterPGInvalidIndex
// when any pg_index row has indisvalid=false, replacing the prior
// warn-continue defense (B2-X-03 backlog). Operators must DROP the invalid
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
		errcode.WithDetails(slog.Int("count", len(indexes))),
		errcode.WithInternal(fmt.Sprintf("invalid indexes: %s", strings.Join(names, ", "))))
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
