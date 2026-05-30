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
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("identifier=%q", name))))
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

// migrationLockTimeout bounds how long any migration statement waits to acquire
// a lock before failing. It is injected at session scope by
// lockTimeoutSessionLocker (see newGooseProvider) so every migration — Up or
// Down, transactional or `-- +goose no transaction` — runs with this bound by
// construction; migration .sql files do not (and must not) set it themselves.
const migrationLockTimeout = "5s"

// DestructiveDownPermit is an explicit break-glass token required for any schema
// rollback. The unexported marker makes the permit sealed: callers outside this
// package cannot fabricate one and must go through AllowDestructiveDown.
type DestructiveDownPermit interface {
	destructiveDownPermit()
	Reason() string
}

type destructiveDownPermit struct {
	reason string
}

// AllowDestructiveDown constructs the explicit permit required by Migrator.Down.
// The reason is kept for audit/log plumbing and must be non-empty.
func AllowDestructiveDown(reason string) (DestructiveDownPermit, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres: destructive migration down requires a non-empty reason")
	}
	return destructiveDownPermit{reason: reason}, nil
}

func (destructiveDownPermit) destructiveDownPermit() {
	// Marker method only seals DestructiveDownPermit to this package.
}

// Reason returns the operator-supplied reason for the destructive rollback.
func (p destructiveDownPermit) Reason() string {
	return p.reason
}

// ForwardRebuildPermit is an explicit break-glass token required to run a
// forward-rebuild migration (DROP+CREATE / TRUNCATE) when the target table
// already holds rows. Like DestructiveDownPermit, the unexported marker seals
// the permit: callers outside this package cannot fabricate one.
type ForwardRebuildPermit interface {
	forwardRebuildPermit()
	MigrationNumber() int64
	Reason() string
}

type forwardRebuildPermit struct {
	migrationNumber int64
	reason          string
}

// AllowForwardRebuild constructs the explicit permit required by
// Migrator.ForwardRebuild for a populated-table rebuild. migrationNumber must
// be positive (goose Source.Version starts at 1) and reason must be non-empty.
func AllowForwardRebuild(migrationNumber int64, reason string) (ForwardRebuildPermit, error) {
	reason = strings.TrimSpace(reason)
	if migrationNumber <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres: forward-rebuild permit requires a positive migration number")
	}
	if reason == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres: forward-rebuild permit requires a non-empty reason")
	}
	return forwardRebuildPermit{migrationNumber: migrationNumber, reason: reason}, nil
}

func (forwardRebuildPermit) forwardRebuildPermit() {}

// MigrationNumber returns the migration version this permit authorizes.
func (p forwardRebuildPermit) MigrationNumber() int64 { return p.migrationNumber }

// Reason returns the operator-supplied reason for the forward rebuild.
func (p forwardRebuildPermit) Reason() string { return p.reason }

// Migrator manages SQL database migrations using goose v3 and an embed.FS source.
// It tracks applied migrations in a configurable table using goose's built-in
// advisory locking.
type Migrator struct {
	provider   *goose.Provider
	db         *sql.DB
	pool       *Pool
	migrations fs.FS
	tableName  string
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

	provider, err := newGooseProvider(db, migrations, tableName)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Migrator{
		provider:   provider,
		db:         db,
		pool:       p,
		migrations: migrations,
		tableName:  tableName,
	}, nil
}

func newGooseProvider(db *sql.DB, migrations fs.FS, tableName string) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: create session locker", err)
	}

	// Always wrap with lock_timeout injection so every applied migration (Up or
	// Down) runs with a bounded lock-wait by construction — see
	// lockTimeoutSessionLocker.
	wrappedLocker := &lockTimeoutSessionLocker{inner: locker}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations,
		goose.WithTableName(tableName),
		goose.WithSessionLocker(wrappedLocker),
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: create goose provider", err)
	}
	return provider, nil
}

// Up applies all unapplied migrations. A pending forward-rebuild migration
// whose target table already holds rows is refused fail-closed (use
// ForwardRebuild with an explicit permit). Pre-checks invalid indexes first.
func (m *Migrator) Up(ctx context.Context) error {
	return m.forwardRun(ctx, nil)
}

// ForwardRebuild applies all unapplied migrations, authorizing the supplied
// forward-rebuild permits for populated target tables. Each permit names the
// migration version it authorizes; a permit referencing a migration that is
// not a pending forward-rebuild is a misconfiguration error.
func (m *Migrator) ForwardRebuild(ctx context.Context, permits ...ForwardRebuildPermit) error {
	return m.forwardRun(ctx, permits)
}

func (m *Migrator) forwardRun(ctx context.Context, permits []ForwardRebuildPermit) error {
	if err := m.checkNoInvalidIndexes(ctx); err != nil {
		return err
	}
	if err := m.gatePendingRebuilds(ctx, permits); err != nil {
		return err
	}
	if _, err := m.provider.Up(ctx); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: apply migrations", err)
	}
	return nil
}

// checkNoInvalidIndexes returns an error if any INVALID indexes are present.
// ref: pressly/goose migration workflow boundary — fail before advancing
// version, not after; same principle as Atlas lint gate.
// ref: golang-migrate Source.Read — validate preconditions before applying.
func (m *Migrator) checkNoInvalidIndexes(ctx context.Context) error {
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
			errcode.WithDetails(errcode.PublicInt("count", len(invalid))),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("indexes=%v", names))))
	}
	return nil
}

// forwardRebuildAnnotationRE matches the per-migration declaration that marks a
// forward-rebuild and names the table whose row-count gates the permit.
var forwardRebuildAnnotationRE = regexp.MustCompile(`(?m)^\s*--\s*\+gocell\s+forward-rebuild\s+target=([a-zA-Z_][a-zA-Z0-9_]*)\s*$`)

func (m *Migrator) gatePendingRebuilds(ctx context.Context, permits []ForwardRebuildPermit) error {
	pending, err := m.collectPendingForwardRebuilds(ctx)
	if err != nil {
		return err
	}

	permitByNum := map[int64]ForwardRebuildPermit{}
	for _, p := range permits {
		permitByNum[p.MigrationNumber()] = p
	}

	// Misconfiguration: every supplied permit must reference a pending forward-rebuild.
	for num := range permitByNum {
		if _, ok := pending[num]; !ok {
			return errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
				"postgres: forward-rebuild permit references a migration that is not a pending forward-rebuild",
				errcode.WithDetails(errcode.PublicInt("migration", num)))
		}
	}

	// Fail-closed: every pending rebuild whose target table holds rows needs a permit.
	for version, target := range pending {
		if err := m.requirePermitIfDangerous(ctx, version, target, permitByNum); err != nil {
			return err
		}
	}
	return nil
}

// collectPendingForwardRebuilds returns the version→target map of pending
// migrations (version > current DB version) that carry the
// `-- +gocell forward-rebuild target=<table>` annotation.
func (m *Migrator) collectPendingForwardRebuilds(ctx context.Context) (map[int64]string, error) {
	current, _, err := m.provider.GetVersions(ctx)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: read migration version", err)
	}
	pending := map[int64]string{}
	for _, src := range m.provider.ListSources() {
		if src.Version <= current {
			continue
		}
		target, ok, perr := m.forwardRebuildTarget(src)
		if perr != nil {
			return nil, perr
		}
		if ok {
			pending[src.Version] = target
		}
	}
	return pending, nil
}

// requirePermitIfDangerous fails closed when the rebuild target table holds rows
// and no permit authorizes the loss. An empty/missing target is always safe.
func (m *Migrator) requirePermitIfDangerous(
	ctx context.Context, version int64, target string, permitByNum map[int64]ForwardRebuildPermit,
) error {
	dangerous, err := m.tableHasRows(ctx, target)
	if err != nil {
		return err
	}
	if !dangerous {
		return nil // empty/missing target — safe to rebuild without a permit
	}
	if _, ok := permitByNum[version]; ok {
		return nil
	}
	return errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
		"postgres: forward-rebuild refused: target table is non-empty and no permit was supplied",
		errcode.WithDetails(
			errcode.PublicInt("migration", version),
			errcode.PublicString("target", target)),
		errcode.WithInternal(errcode.InternalAttr("_",
			fmt.Sprintf("call ForwardRebuild with AllowForwardRebuild(%d, reason)", version))))
}

// forwardRebuildTarget reads the migration source and extracts the
// `-- +gocell forward-rebuild target=<table>` annotation, if present.
func (m *Migrator) forwardRebuildTarget(src *goose.Source) (string, bool, error) {
	data, err := fs.ReadFile(m.migrations, src.Path)
	if err != nil {
		return "", false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate,
			"postgres: read migration source for rebuild gate", err)
	}
	mch := forwardRebuildAnnotationRE.FindSubmatch(data)
	if mch == nil {
		return "", false, nil
	}
	return string(mch[1]), true, nil
}

// tableHasRows reports whether table exists and holds at least one row. A
// missing table reports false (a fresh DB is always safe to rebuild). The table
// name is validated as a SQL identifier because it cannot be parameterised in
// the FROM position.
func (m *Migrator) tableHasRows(ctx context.Context, table string) (bool, error) {
	if err := validateIdentifier(table); err != nil {
		return false, err
	}
	var exists bool
	if err := m.pool.DB().QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: probe rebuild target existence", err)
	}
	if !exists {
		return false, nil
	}
	var hasRows bool
	// #nosec G201 -- table validated by validateIdentifier above; cannot parameterise FROM.
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s)`, table)
	if err := m.pool.DB().QueryRow(ctx, q).Scan(&hasRows); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: probe rebuild target rows", err)
	}
	return hasRows, nil
}

// Down rolls back the last applied migration. If no migrations have been
// applied (version 0), Down is a no-op and returns nil. Callers must pass an
// explicit DestructiveDownPermit because rollback files may drop production
// data even when they only move the schema back by one version.
func (m *Migrator) Down(ctx context.Context, permit DestructiveDownPermit) error {
	if permit == nil || strings.TrimSpace(permit.Reason()) == "" {
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

// lockTimeoutSessionLocker wraps an inner SessionLocker and sets lock_timeout
// at session scope for the duration of the migration session, then resets it on
// unlock. newGooseProvider wraps every provider with this locker, so lock_timeout
// is applied to every migration — Up or Down, transactional or
// `-- +goose no transaction` — by construction.
//
// AI-robust rating: Medium (not Hard). The funnel rests on three facts:
//   - newGooseProvider always wraps the resolved locker with this type (one
//     code line below) — code-fact, not separately archtested;
//   - newGooseProvider is the sole construction site for a provider that APPLIES
//     migrations (Migrator.Up / Migrator.Down) — GOOSE-SESSION-LOCKER-01 pins
//     every *mutating* goose.NewProvider callsite under adapters/postgres/ to
//     carry WithSessionLocker and carves out schema_guard.VerifyExpectedVersion's
//     read-only (GetDBVersion, no DDL) provider;
//   - TestMigrator_LockTimeoutSessionLocker_SetsAndResets verifies the locker's
//     set/reset behavior.
//
// True Hard (type-seal the provider so it cannot be constructed without
// lock_timeout) is INFEASIBLE: goose.NewProvider is a third-party constructor
// that cannot be sealed, so a future sibling caller inside package postgres
// could in principle build a mutating provider without this wrapper — caught by
// GOOSE-SESSION-LOCKER-01 (Medium, caller-scope) + review, not by the type
// system. This is a permanent Medium ceiling, tracked for Hard-ification in
// gh issue #1131 (alongside the SPAN #851 / HEALTHZ #893 holder-seal precedents).
// The only operational bypass is running goose CLI / psql directly.
//
// Session scope (set_config third arg = false) — NOT SET LOCAL — is required so
// the timeout survives across the implicit-transaction boundaries of
// `-- +goose no transaction` migrations.
// SessionUnlock resets via RESET so the connection is clean when returned to the
// shared pgxpool (the migrator's *sql.DB wraps the same pool the app uses).
type lockTimeoutSessionLocker struct {
	inner lock.SessionLocker
}

func (l *lockTimeoutSessionLocker) SessionLock(ctx context.Context, conn *sql.Conn) (retErr error) {
	if err := l.inner.SessionLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, l.inner.SessionUnlock(context.WithoutCancel(ctx), conn))
		}
	}()
	if _, err := conn.ExecContext(ctx,
		`SELECT set_config('lock_timeout', $1, false)`, migrationLockTimeout); err != nil {
		return fmt.Errorf("postgres: set migration lock_timeout: %w", err)
	}
	return nil
}

func (l *lockTimeoutSessionLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	resetCtx := context.WithoutCancel(ctx)
	// RESET (not set_config to '') restores lock_timeout to the server/startup
	// default; lock_timeout is a typed GUC for which '' is not a valid value.
	_, resetErr := conn.ExecContext(resetCtx, `RESET lock_timeout`)
	unlockErr := l.inner.SessionUnlock(resetCtx, conn)
	return errors.Join(resetErr, unlockErr)
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
