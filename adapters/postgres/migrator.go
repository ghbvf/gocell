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
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/pkg/validation"
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
// ns identifies the migration lineage; the goose tracking table is derived as
// schema_migrations_<namespace> (see trackingTableFor). Passing a typed
// migration.Namespace — not a free table-name string — makes "track migrations
// in the bare global schema_migrations table" (the #1089 / R3 collision
// footgun) unexpressible by construction. ns must be valid (fail-fast).
func NewMigrator(p *Pool, migrations fs.FS, ns migration.Namespace) (*Migrator, error) {
	if err := ns.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, ErrAdapterPGMigrate,
			"postgres: invalid migration namespace", err)
	}
	return newMigratorForTable(p, migrations, trackingTableFor(ns))
}

// newMigratorForTable builds a Migrator against an explicit goose tracking table.
// It is the unexported construction core; the only production caller is
// NewMigrator, which derives the table from a migration.Namespace via
// trackingTableFor (so the exported surface never accepts a free table string —
// the #1089 / R3 seal). In-package tests call it directly to provision isolated
// per-test tracking tables. The non-test caller allowlist (= NewMigrator only)
// is held by archtest MIGRATION-TRACKING-TABLE-DERIVED-01.
func newMigratorForTable(p *Pool, migrations fs.FS, tableName string) (*Migrator, error) {
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
// forward-rebuild permits for populated target tables.
//
// A permit is only needed when a forward-rebuild migration's target table
// already holds rows — an empty or missing target is rebuilt automatically by
// Up alone, so fresh provisioning needs no permit. Supplying more permits than
// required is fine; a permit that is nil, duplicated, or references a migration
// which is not a pending forward-rebuild is a misconfiguration error.
func (m *Migrator) ForwardRebuild(ctx context.Context, permits ...ForwardRebuildPermit) error {
	return m.forwardRun(ctx, permits)
}

func (m *Migrator) forwardRun(ctx context.Context, permits []ForwardRebuildPermit) error {
	// Strict precheck (#1089 / codex C2): reject a migration FS that carries a
	// non-goose-parseable .sql before applying, so a malformed/misnamed file
	// fails fast (naming it) instead of being silently skipped by goose.
	if _, err := ExpectedVersion(m.migrations); err != nil {
		return err
	}
	if err := m.checkNoLegacyPlatformTable(ctx); err != nil {
		return err
	}
	if err := m.checkNoInvalidIndexes(ctx); err != nil {
		return err
	}
	if err := m.gatePendingRebuilds(ctx, permits); err != nil {
		return err
	}
	results, err := m.provider.Up(ctx)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: apply migrations", err)
	}
	if len(results) == 0 {
		// tracking_table = schema_migrations_<namespace>, so a MigrationSet.ApplyAll
		// over multiple namespaces stays attributable in the log stream.
		slog.InfoContext(ctx, "postgres: migrations already up to date",
			"tracking_table", m.tableName, "applied", 0)
		return nil
	}
	var finalVersion int64
	for _, r := range results {
		if r.Source != nil && r.Source.Version > finalVersion {
			finalVersion = r.Source.Version
		}
	}
	slog.InfoContext(ctx, "postgres: migrations applied",
		"tracking_table", m.tableName,
		"applied", len(results),
		"final_version", finalVersion)
	return nil
}

// legacyPlatformTrackingTable is the pre-#1089 goose tracking table name for the
// platform migration set, renamed to PlatformTrackingTable
// (schema_migrations_platform). It is referenced only by the transition guard.
const legacyPlatformTrackingTable = "schema_migrations"

// checkNoLegacyPlatformTable fails fast when applying the platform migration set
// (m.tableName == PlatformTrackingTable) against a database that still carries
// the pre-#1089 `schema_migrations` tracking table but NOT the renamed
// `schema_migrations_platform`. In that state goose would treat the platform
// lineage as version 0 and re-apply every migration against an already-populated
// schema — a confusing cascade of "already exists" errors. Pre-v1.0 there is no
// production database and ephemeral test DBs are recreated fresh each run, so
// this only guards a developer's long-lived local DB; the message tells them how
// to recover. Non-platform namespaces and fresh DBs are a no-op.
//
// codex C3 minimal方案 (fail-fast detect legacy table); the project's accepted
// transition policy is "recreate the ephemeral DB" (#1089 decision #3).
func (m *Migrator) checkNoLegacyPlatformTable(ctx context.Context) error {
	if m.tableName != PlatformTrackingTable {
		return nil
	}
	var legacyExists bool
	if err := m.pool.DB().QueryRow(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, legacyPlatformTrackingTable).Scan(&legacyExists); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate,
			"postgres: probe legacy tracking table", err)
	}
	if !legacyExists {
		return nil
	}
	var newExists bool
	if err := m.pool.DB().QueryRow(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, m.tableName).Scan(&newExists); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate,
			"postgres: probe platform tracking table", err)
	}
	if newExists {
		return nil // both present — already transitioned / coexisting; not this guard's concern
	}
	return errcode.New(errcode.KindInternal, ErrAdapterPGMigrate,
		"postgres: legacy goose tracking table \"schema_migrations\" present without "+
			"\"schema_migrations_platform\" — the platform tracking table was renamed (#1089). "+
			"Recreate the ephemeral database, or run "+
			"`ALTER TABLE schema_migrations RENAME TO schema_migrations_platform`.")
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

// ForwardRebuildAnnotationPattern is the single-source regex shared by the
// runtime permit gate (forwardRebuildTargets) and the archtest
// MIGRATION-FORWARD-REBUILD-ANNOTATION-01
// (tools/archtest/pg_schema_guard_invariants_test.go). Exporting it as one const
// keeps the Go gate and the CI archtest from drifting.
//
// Each annotation line names exactly ONE table. A migration that rebuilds
// multiple tables uses multiple annotation lines — one per table. The Go phase0
// gate scans all matching lines with FindAllSubmatch and gates each target
// independently.
//
// WARNING: changing this pattern is a wire-format change to the migration
// annotation. Every existing `-- +gocell forward-rebuild target=…` line in
// adapters/postgres/migrations/*.sql must be updated to match, or pending
// rebuilds silently stop being gated.
const ForwardRebuildAnnotationPattern = `(?m)^\s*--\s*\+gocell\s+forward-rebuild\s+target=([a-zA-Z_][a-zA-Z0-9_]*)\s*$`

// forwardRebuildAnnotationRE matches each `-- +gocell forward-rebuild target=<table>`
// annotation line in the Up section of a migration. FindAllSubmatch returns one
// entry per annotation line, supporting migrations that rebuild multiple tables.
var forwardRebuildAnnotationRE = regexp.MustCompile(ForwardRebuildAnnotationPattern)

// gooseDownMarkerRE line-anchors the pressly/goose Down section directive so the
// Up-section scan in forwardRebuildTargets cannot be truncated by a literal
// "-- +goose Down" appearing inside Up-section prose (fail-open guard).
var gooseDownMarkerRE = regexp.MustCompile(`(?m)^\s*-- \+goose Down\s*$`)

func (m *Migrator) gatePendingRebuilds(ctx context.Context, permits []ForwardRebuildPermit) error {
	pending, err := m.collectPendingForwardRebuilds(ctx)
	if err != nil {
		return err
	}

	permitByNum, err := buildPermitMap(permits)
	if err != nil {
		return err
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
	// A migration with multiple targets requires each populated target to be covered
	// by the same permit (one permit per migration number, not per table).
	for version, targets := range pending {
		for _, target := range targets {
			if err := m.requirePermitIfDangerous(ctx, version, target, permitByNum); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildPermitMap indexes the supplied permits by migration number. It rejects a
// nil interface value (typed-nil included, mirroring Down's guard) and rejects
// two permits naming the same migration — both are caller misconfigurations
// that would otherwise panic or silently last-write-win.
func buildPermitMap(permits []ForwardRebuildPermit) (map[int64]ForwardRebuildPermit, error) {
	permitByNum := make(map[int64]ForwardRebuildPermit, len(permits))
	for _, p := range permits {
		if p == nil || validation.IsNilInterface(p) {
			return nil, errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
				"postgres: forward-rebuild permit must not be nil")
		}
		num := p.MigrationNumber()
		if _, dup := permitByNum[num]; dup {
			return nil, errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
				"postgres: forward-rebuild misconfiguration: duplicate permit for the same migration",
				errcode.WithDetails(errcode.PublicInt("migration", num)))
		}
		permitByNum[num] = p
	}
	return permitByNum, nil
}

// collectPendingForwardRebuilds returns the version→targets map of pending
// migrations (version > current DB version) that carry at least one
// `-- +gocell forward-rebuild target=<table>` annotation. Each migration may
// name multiple targets; each target must be independently permitted if its
// table is non-empty.
func (m *Migrator) collectPendingForwardRebuilds(ctx context.Context) (map[int64][]string, error) {
	current, _, err := m.provider.GetVersions(ctx)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: read migration version", err)
	}
	pending := map[int64][]string{}
	for _, src := range m.provider.ListSources() {
		if src.Version <= current {
			continue
		}
		targets, ok, perr := m.forwardRebuildTargets(src)
		if perr != nil {
			return nil, perr
		}
		if ok {
			pending[src.Version] = targets
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
	permit, ok := permitByNum[version]
	if ok {
		slog.InfoContext(ctx, "postgres: forward-rebuild authorized",
			"tracking_table", m.tableName,
			"migration", version,
			"target", target,
			"reason", permit.Reason())
		return nil
	}
	return errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
		"postgres: forward-rebuild refused: target table is non-empty and no permit was supplied",
		errcode.WithDetails(
			errcode.PublicInt("migration", version),
			errcode.PublicString("target", target)),
		errcode.WithInternal(errcode.InternalAttr("_",
			fmt.Sprintf(
				"authorize via: pg-migrate -dsn \"$GOCELL_PG_DSN\" -rebuild %d:<reason>  "+
					"(or AllowForwardRebuild(%d, \"<reason>\") in Go)",
				version, version))))
}

// forwardRebuildTargets reads the migration source and extracts all
// `-- +gocell forward-rebuild target=<table>` annotations from the Up section
// only. The Down section is excluded so annotations accidentally placed there
// do not affect the permit gate.
//
// A migration that rebuilds multiple tables uses one annotation line per table;
// this function returns all annotated targets. Returns (nil, false, nil) when
// no annotation is found.
func (m *Migrator) forwardRebuildTargets(src *goose.Source) ([]string, bool, error) {
	data, err := fs.ReadFile(m.migrations, src.Path)
	if err != nil {
		return nil, false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate,
			"postgres: read migration source for rebuild gate", err)
	}
	// Restrict scan to the Up section: content before the goose Down marker.
	// Line-anchored (^...$) so a literal "-- +goose Down" appearing inside
	// Up-section prose / runbook comments does NOT truncate the scan and silently
	// drop the annotation (fail-open → populated table rebuilt without a permit).
	// "-- +goose Down" is a pressly/goose section directive honored only at the
	// start of a line.
	up := data
	if loc := gooseDownMarkerRE.FindIndex(data); loc != nil {
		up = data[:loc[0]]
	}
	all := forwardRebuildAnnotationRE.FindAllSubmatch(up, -1)
	if len(all) == 0 {
		return nil, false, nil
	}
	targets := make([]string, len(all))
	for i, mch := range all {
		targets[i] = string(mch[1])
	}
	return targets, true, nil
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
	// permit.Reason() is guaranteed non-empty by AllowDestructiveDown — the sole
	// constructor of the sealed DestructiveDownPermit — so only the nil guard is
	// needed; a separate empty-reason branch would be unreachable dead code.
	if permit == nil || validation.IsNilInterface(permit) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres: destructive migration down requires explicit permit")
	}
	if _, err := m.provider.Down(ctx); err != nil {
		if errors.Is(err, goose.ErrNoCurrentVersion) || errors.Is(err, goose.ErrNoNextVersion) {
			return nil // already at version 0, idempotent no-op
		}
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate, "postgres: rollback migration", err)
	}
	slog.InfoContext(ctx, "postgres: migration rolled back",
		"tracking_table", m.tableName, "reason", permit.Reason())
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
