package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// identifierRe matches valid SQL identifiers: start with letter or underscore,
// followed by letters, digits, or underscores.
var identifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validateIdentifier checks that name is a safe SQL identifier to prevent
// SQL injection when used in table-name positions (which cannot be
// parameterised).
func validateIdentifier(name string) error {
	if !identifierRe.MatchString(name) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid SQL identifier",
			errcode.WithInternal(fmt.Sprintf("identifier=%q", name)))
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

// DestructiveDownPermit is an explicit break-glass token required for any
// schema rollback. Down migrations can drop user, session, and role data; this
// value makes accidental rollback calls impossible at the API boundary.
type DestructiveDownPermit struct {
	reason string
}

// AllowDestructiveDown constructs the explicit permit required by Migrator.Down.
// The reason is kept for audit/log plumbing and must be non-empty.
func AllowDestructiveDown(reason string) (DestructiveDownPermit, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return DestructiveDownPermit{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres: destructive migration down requires a non-empty reason")
	}
	return DestructiveDownPermit{reason: reason}, nil
}

// Reason returns the operator-supplied reason for the destructive rollback.
func (p DestructiveDownPermit) Reason() string {
	return p.reason
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

	// SessionLocker holds a pg_advisory_lock for the duration of Up/Down so
	// concurrent migrators (multi-pod startup) serialize on the lock rather
	// than racing the schema_migrations table. Default lockID is goose's
	// constant 4097083626 (CRC of "goose"), which the codebase does not use
	// elsewhere. Defaults give a 5min acquire budget (60 retries × 5s
	// pg_try_advisory_lock); the ctx passed to Up/Down has priority — if it
	// is canceled before the budget is exhausted, retry.Do returns
	// immediately, so the caller's startup deadline always wins.
	//
	// ref: pressly/goose lock/postgres.go pg_try_advisory_lock + retry
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_ = db.Close()
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: create session locker", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations,
		goose.WithTableName(tableName),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		_ = db.Close()
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: create goose provider", err)
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
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: pre-check invalid indexes", err)
	}
	if len(invalid) > 0 {
		names := make([]string, len(invalid))
		for i, idx := range invalid {
			names[i] = idx.Index
		}
		return errcode.New(errcode.KindInternal, ErrAdapterPGMigrate,
			"postgres: refusing to migrate: invalid indexes detected",
			errcode.WithDetails(slog.Int("count", len(invalid))),
			errcode.WithInternal(fmt.Sprintf("indexes=%v", names)))
	}
	if _, err := m.provider.Up(ctx); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: apply migrations", err)
	}
	return nil
}

// Down rolls back the last applied migration. If no migrations have been
// applied (version 0), Down is a no-op and returns nil. Callers must pass an
// explicit DestructiveDownPermit because rollback files may drop production
// data even when they only move the schema back by one version.
func (m *Migrator) Down(ctx context.Context, permit DestructiveDownPermit) error {
	if strings.TrimSpace(permit.reason) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres: destructive migration down requires explicit permit")
	}
	if _, err := m.provider.Down(ctx); err != nil {
		if errors.Is(err, goose.ErrNoCurrentVersion) || errors.Is(err, goose.ErrNoNextVersion) {
			return nil // already at version 0, idempotent no-op
		}
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: rollback migration", err)
	}
	return nil
}

// Status returns the status of all discovered migrations.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	results, err := m.provider.Status(ctx)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: query migration status", err)
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
// "001_create_outbox_entries.sql" → "create_outbox_entries".
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
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: close migrator db", err)
	}
	return nil
}
