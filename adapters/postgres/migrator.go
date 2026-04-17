package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

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

// Migrator manages SQL database migrations using goose v3 and an embed.FS source.
// It tracks applied migrations in a configurable table using goose's built-in
// advisory locking.
type Migrator struct {
	provider  *goose.Provider
	db        *sql.DB
	pool      *Pool
	tableName string
}

// NewMigrator creates a Migrator that reads SQL files from the given fs.FS.
// Migration files must follow the goose annotated format with -- +goose Up
// and -- +goose Down sections.
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

	db := stdlib.OpenDBFromPool(p.inner)

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations,
		goose.WithTableName(tableName),
	)
	if err != nil {
		_ = db.Close()
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: create goose provider", err)
	}

	return &Migrator{
		provider:  provider,
		db:        db,
		pool:      p,
		tableName: tableName,
	}, nil
}

// Up applies all unapplied migrations in order.
// It performs a pre-check for INVALID indexes before advancing the schema
// version: if any index is found with indisvalid=false, Up returns an error
// and does not execute any migrations. Manual cleanup is required before
// re-running.
//
// ref: pressly/goose migration workflow boundary — fail before advancing
// version, not after; same principle as Atlas lint gate.
// ref: golang-migrate Source.Read — validate preconditions before applying.
func (m *Migrator) Up(ctx context.Context) error {
	invalid, err := DetectInvalidIndexes(ctx, m.pool)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: pre-check invalid indexes", err)
	}
	if len(invalid) > 0 {
		names := make([]string, len(invalid))
		for i, idx := range invalid {
			names[i] = idx.Index
		}
		return errcode.New(ErrAdapterPGMigrate,
			fmt.Sprintf("postgres: refusing to migrate: %d invalid index(es) detected: %v; manual cleanup required before proceeding", len(invalid), names))
	}
	if _, err := m.provider.Up(ctx); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: apply migrations", err)
	}
	return nil
}

// Down rolls back the last applied migration. If no migrations have been
// applied (version 0), Down is a no-op and returns nil.
func (m *Migrator) Down(ctx context.Context) error {
	if _, err := m.provider.Down(ctx); err != nil {
		if errors.Is(err, goose.ErrNoCurrentVersion) || errors.Is(err, goose.ErrNoNextVersion) {
			return nil // already at version 0, idempotent no-op
		}
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: rollback migration", err)
	}
	return nil
}

// Status returns the status of all discovered migrations.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	results, err := m.provider.Status(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMigrate, "postgres: query migration status", err)
	}

	statuses := make([]MigrationStatus, 0, len(results))
	for _, r := range results {
		ms := MigrationStatus{
			Version: fmt.Sprintf("%03d", r.Source.Version),
			Name:    migrationName(r.Source.Path, r.Source.Version),
			Applied: r.State == goose.StateApplied,
		}
		if ms.Applied && !r.AppliedAt.IsZero() {
			ms.AppliedAt = r.AppliedAt
		}
		statuses = append(statuses, ms)
	}
	return statuses, nil
}

// migrationName extracts the descriptive name from a goose migration path.
// "001_create_outbox_entries.sql" → "create_outbox_entries"
func migrationName(path string, version int64) string {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	prefix := fmt.Sprintf("%03d_", version)
	name := strings.TrimPrefix(base, prefix)
	name = strings.TrimSuffix(name, ".sql")
	return name
}

// Close releases the underlying *sql.DB created for goose.
func (m *Migrator) Close() error {
	if err := m.db.Close(); err != nil {
		return errcode.Wrap(ErrAdapterPGMigrate, "postgres: close migrator db", err)
	}
	return nil
}
