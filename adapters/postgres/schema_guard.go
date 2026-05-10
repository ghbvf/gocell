package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ghbvf/gocell/pkg/errcode"
)

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
// matches the binary's expectation. Run **after** VerifyExpectedVersion
// (which gates migration version) — VerifyExpectedShape catches
// "version table says N but my migration's DDL never reached the column"
// drift, e.g. partial migration that did not abort, or a 3rd-party tool
// applying SQL out-of-band.
//
// ADR-credential §5.1.3 deployment playbook mandates these checks for the
// S3+S5 schema (users.authz_epoch, sessions.jti, sessions.access_token must
// NOT exist). Each fault returns ErrAdapterPGSchemaShape so operators see
// the precise column at fault.
func VerifyExpectedShape(ctx context.Context, pool *Pool) error {
	required := []requiredColumn{
		{table: "users", column: "authz_epoch"},
		{table: "sessions", column: "jti"},
		{table: "sessions", column: "subject_id"},
		{table: "sessions", column: "authz_epoch_at_issue"},
		{table: "role_assignments", column: "user_id"},
	}
	for _, r := range required {
		ok, err := columnExists(ctx, pool, r.table, r.column)
		if err != nil {
			return err
		}
		if !ok {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: required column missing",
				errcode.WithDetails(slog.String("table", r.table), slog.String("column", r.column)))
		}
	}
	// Forbidden columns: ADR-credential D1 forbids plaintext token storage.
	// The legacy `sessions.access_token` column must not exist after
	// migration 018; its presence indicates a partial / aborted migration.
	forbidden := []requiredColumn{
		{table: "sessions", column: "access_token"},
	}
	for _, r := range forbidden {
		ok, err := columnExists(ctx, pool, r.table, r.column)
		if err != nil {
			return err
		}
		if ok {
			return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"schema_guard: forbidden legacy column present (partial migration)",
				errcode.WithDetails(slog.String("table", r.table), slog.String("column", r.column)))
		}
	}
	return nil
}

// requiredColumn pairs a (table, column) tuple for VerifyExpectedShape.
type requiredColumn struct {
	table  string
	column string
}

// columnExists is the predicate behind VerifyExpectedShape. Scoped to
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
// CREATE INDEX CONCURRENTLY is interrupted. The caller should log a warning
// and consider manual cleanup.
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
		WHERE NOT i.indisvalid
		  AND n.nspname = current_schema()`

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
