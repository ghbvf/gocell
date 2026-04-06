package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockID is a fixed PostgreSQL advisory lock ID used to prevent
// concurrent migration execution across multiple processes.
const migrationLockID int64 = 1234567890

// identifierRe matches valid SQL identifiers: start with letter or underscore,
// followed by letters, digits, or underscores.
var identifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validateIdentifier checks that name is a safe SQL identifier to prevent
// SQL injection when used in table-name positions (which cannot be
// parameterised).
func validateIdentifier(name string) error {
	if !identifierRe.MatchString(name) {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("invalid SQL identifier: %q", name))
	}
	return nil
}

// MigrationDirection indicates whether a migration is applied or rolled back.
type MigrationDirection string

const (
	// MigrationUp applies a migration.
	MigrationUp MigrationDirection = "up"
	// MigrationDown rolls back a migration.
	MigrationDown MigrationDirection = "down"
)

// MigrationStatus describes the state of a single migration file.
type MigrationStatus struct {
	// Version is the migration prefix (e.g. "001").
	Version string
	// Name is the descriptive part (e.g. "create_outbox_entries").
	Name string
	// Applied indicates whether this migration has been executed.
	Applied bool
	// AppliedAt is when the migration was applied (zero if not applied).
	AppliedAt time.Time
}

// Migrator manages SQL database migrations using an embed.FS source.
// It tracks applied migrations in a schema_migrations table using advisory
// locking to prevent concurrent execution (adopted from Watermill's approach).
type Migrator struct {
	pool       *pgxpool.Pool
	migrations fs.FS
	tableName  string
}

// NewMigrator creates a Migrator that reads SQL files from the given fs.FS.
// Migration files must follow the naming convention:
//
//	{version}_{name}.up.sql
//	{version}_{name}.down.sql
//
// The tableName parameter controls the tracking table name (default:
// "schema_migrations"). It must be a valid SQL identifier
// ([a-zA-Z_][a-zA-Z0-9_]*) to prevent SQL injection.
func NewMigrator(p *Pool, migrations fs.FS, tableName string) (*Migrator, error) {
	if tableName == "" {
		tableName = "schema_migrations"
	}
	if err := validateIdentifier(tableName); err != nil {
		return nil, err
	}
	return &Migrator{
		pool:       p.inner,
		migrations: migrations,
		tableName:  tableName,
	}, nil
}

// ensureTable creates the schema_migrations table if it does not exist.
func (m *Migrator) ensureTable(ctx context.Context) error {
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		version    TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`, m.tableName)

	if _, err := m.pool.Exec(ctx, query); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: create migrations table", err)
	}
	return nil
}

// Up applies all unapplied migrations in order.
// It acquires a PostgreSQL advisory lock to prevent concurrent migration
// execution across multiple processes.
func (m *Migrator) Up(ctx context.Context) error {
	if _, err := m.pool.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: acquire migration advisory lock", err)
	}
	defer m.pool.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID) //nolint:errcheck

	if err := m.ensureTable(ctx); err != nil {
		return err
	}

	files, err := m.listMigrations(MigrationUp)
	if err != nil {
		return err
	}

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, mf := range files {
		if applied[mf.version] {
			continue
		}
		if err := m.applyMigration(ctx, mf); err != nil {
			return err
		}
	}
	return nil
}

// Down rolls back the last applied migration.
// It acquires a PostgreSQL advisory lock to prevent concurrent migration
// execution across multiple processes.
func (m *Migrator) Down(ctx context.Context) error {
	if _, err := m.pool.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: acquire migration advisory lock", err)
	}
	defer m.pool.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID) //nolint:errcheck

	if err := m.ensureTable(ctx); err != nil {
		return err
	}

	latest, err := m.latestApplied(ctx)
	if err != nil {
		return err
	}
	if latest == "" {
		slog.Info("postgres: no migrations to roll back")
		return nil
	}

	files, err := m.listMigrations(MigrationDown)
	if err != nil {
		return err
	}

	for _, mf := range files {
		if mf.version == latest {
			return m.rollbackMigration(ctx, mf)
		}
	}

	return errcode.New(ErrAdapterPGMigrate,
		fmt.Sprintf("postgres: down migration not found for version %s", latest))
}

// Status returns the status of all discovered migrations.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}

	files, err := m.listMigrations(MigrationUp)
	if err != nil {
		return nil, err
	}

	applied, err := m.appliedDetails(ctx)
	if err != nil {
		return nil, err
	}

	statuses := make([]MigrationStatus, 0, len(files))
	for _, mf := range files {
		ms := MigrationStatus{
			Version: mf.version,
			Name:    mf.name,
		}
		if detail, ok := applied[mf.version]; ok {
			ms.Applied = true
			ms.AppliedAt = detail
		}
		statuses = append(statuses, ms)
	}
	return statuses, nil
}

// migrationFile represents a parsed migration file from the FS.
type migrationFile struct {
	version   string
	name      string
	direction MigrationDirection
	filename  string
}

// listMigrations reads the FS and returns sorted migration files for the given direction.
func (m *Migrator) listMigrations(dir MigrationDirection) ([]migrationFile, error) {
	suffix := fmt.Sprintf(".%s.sql", dir)
	entries, err := fs.ReadDir(m.migrations, ".")
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: read migrations directory", err)
	}

	var files []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		mf, ok := parseMigrationFilename(e.Name(), dir)
		if !ok {
			continue
		}
		files = append(files, mf)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})
	return files, nil
}

// parseMigrationFilename extracts version and name from a filename like
// "001_create_outbox_entries.up.sql".
func parseMigrationFilename(filename string, dir MigrationDirection) (migrationFile, bool) {
	suffix := fmt.Sprintf(".%s.sql", dir)
	if !strings.HasSuffix(filename, suffix) {
		return migrationFile{}, false
	}
	base := strings.TrimSuffix(filename, suffix)
	idx := strings.Index(base, "_")
	if idx < 1 {
		return migrationFile{}, false
	}
	return migrationFile{
		version:   base[:idx],
		name:      base[idx+1:],
		direction: dir,
		filename:  filename,
	}, true
}

// appliedVersions returns a set of already-applied migration versions.
func (m *Migrator) appliedVersions(ctx context.Context) (map[string]bool, error) {
	query := fmt.Sprintf("SELECT version FROM %s", m.tableName)
	rows, err := m.pool.Query(ctx, query)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: query applied migrations", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: scan migration version", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: iterate applied migrations", err)
	}
	return applied, nil
}

// appliedDetails returns applied versions with their timestamps.
func (m *Migrator) appliedDetails(ctx context.Context) (map[string]time.Time, error) {
	query := fmt.Sprintf("SELECT version, applied_at FROM %s", m.tableName)
	rows, err := m.pool.Query(ctx, query)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: query applied details", err)
	}
	defer rows.Close()

	details := make(map[string]time.Time)
	for rows.Next() {
		var v string
		var at time.Time
		if err := rows.Scan(&v, &at); err != nil {
			return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: scan migration detail", err)
		}
		details[v] = at
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: iterate applied details", err)
	}
	return details, nil
}

// latestApplied returns the version of the most recently applied migration.
func (m *Migrator) latestApplied(ctx context.Context) (string, error) {
	query := fmt.Sprintf("SELECT version FROM %s ORDER BY version DESC LIMIT 1", m.tableName)
	var v string
	err := m.pool.QueryRow(ctx, query).Scan(&v)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return "", nil
		}
		return "", errcode.Wrap(ErrAdapterPGMigrate, "postgres: query latest migration", err)
	}
	return v, nil
}

// applyMigration reads and executes an up migration, then records it.
func (m *Migrator) applyMigration(ctx context.Context, mf migrationFile) error {
	sql, err := fs.ReadFile(m.migrations, mf.filename)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: read migration %s", mf.filename), err)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: begin migration tx", err)
	}
	defer func() {
		// Rollback is a no-op if tx was already committed.
		// Use WithoutCancel so rollback succeeds even if caller ctx is cancelled.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: execute migration %s", mf.filename), err)
	}

	insertQuery := fmt.Sprintf(
		"INSERT INTO %s (version, name) VALUES ($1, $2)", m.tableName)
	if _, err := tx.Exec(ctx, insertQuery, mf.version, mf.name); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: record migration %s", mf.version), err)
	}

	if err := tx.Commit(ctx); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: commit migration %s", mf.version), err)
	}

	slog.Info("postgres: migration applied",
		slog.String("version", mf.version),
		slog.String("name", mf.name),
	)
	return nil
}

// rollbackMigration reads and executes a down migration, then removes the record.
func (m *Migrator) rollbackMigration(ctx context.Context, mf migrationFile) error {
	sql, err := fs.ReadFile(m.migrations, mf.filename)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: read down migration %s", mf.filename), err)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: begin rollback tx", err)
	}
	defer func() {
		// Use WithoutCancel so rollback succeeds even if caller ctx is cancelled.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: execute down migration %s", mf.filename), err)
	}

	deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE version = $1", m.tableName)
	if _, err := tx.Exec(ctx, deleteQuery, mf.version); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: remove migration record %s", mf.version), err)
	}

	if err := tx.Commit(ctx); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: commit rollback %s", mf.version), err)
	}

	slog.Info("postgres: migration rolled back",
		slog.String("version", mf.version),
		slog.String("name", mf.name),
	)
	return nil
}
