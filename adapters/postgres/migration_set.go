package postgres

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/migration"
)

// trackingTablePrefix is the fixed prefix for every per-namespace goose tracking
// table. trackingTableFor is the SINGLE source that derives a table name from a
// namespace; no other code path constructs a tracking-table string.
const trackingTablePrefix = "schema_migrations_"

// trackingTableFor derives the goose version-tracking table for a namespace:
// schema_migrations_<namespace>. The namespace is a migration.Namespace
// (validated lowercase identifier, bounded length), so the result is always a
// safe SQL identifier within PostgreSQL's 63-byte limit. This is the only
// function that builds a tracking-table name — guarded by archtest
// MIGRATION-TRACKING-TABLE-DERIVED-01 so newGooseProvider's tableName argument
// can never be an inlined literal.
func trackingTableFor(ns migration.Namespace) string {
	return trackingTablePrefix + ns.String()
}

// PlatformTrackingTable is the tracking table for the platform's own embedded
// migrations: schema_migrations_platform. Platform is not special — it is the
// reserved migration.PlatformNamespace flowing through the same derivation as
// every external namespace (full symmetry, #1089 decision #3).
var PlatformTrackingTable = trackingTableFor(migration.PlatformNamespace)

// MigrationSet is an ordered registry of per-namespace migration sources. Each
// namespace owns an independent 001..N version lineage tracked in its own
// schema_migrations_<namespace> table (Django-style (namespace, version)
// composite key, #1089). There is no global version sequence: namespaces are
// independent, applied in registration order.
//
// ref: pressly/goose v3 Provider.WithTableName — per-table version counter.
type MigrationSet struct {
	entries []migrationSetEntry
	seen    map[migration.Namespace]struct{}
}

type migrationSetEntry struct {
	ns   migration.Namespace
	fsys fs.FS
}

// NewMigrationSet returns an empty MigrationSet. Use NewMigrationSetWithPlatform
// for the common case (platform + external namespaces).
func NewMigrationSet() *MigrationSet {
	return &MigrationSet{seen: map[migration.Namespace]struct{}{}}
}

// NewMigrationSetWithPlatform returns a MigrationSet pre-seeded with the
// platform namespace (adapters/postgres embedded migrations) FIRST. Platform
// applies before any external namespace registered via Add, so an external
// Cell module's migrations may reference platform tables (e.g. foreign keys) —
// platform-first ordering is the concrete cross-namespace guarantee.
func NewMigrationSetWithPlatform() (*MigrationSet, error) {
	fsys, err := MigrationsFS()
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMigrate,
			"postgres: load platform migrations fs", err)
	}
	s := NewMigrationSet()
	if err := s.addNamespace(migration.PlatformNamespace, fsys); err != nil {
		return nil, err
	}
	return s, nil
}

// Add registers an external Cell module's migration set under ns. The reserved
// platform namespace is rejected (only the internal seed in
// NewMigrationSetWithPlatform may use it); ns must be valid and fsys non-nil;
// duplicate namespaces are rejected.
func (s *MigrationSet) Add(ns migration.Namespace, fsys fs.FS) error {
	if ns == migration.PlatformNamespace {
		return errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
			"postgres: namespace \"platform\" is reserved for platform migrations; "+
				"external modules must register under their own namespace")
	}
	return s.addNamespace(ns, fsys)
}

func (s *MigrationSet) addNamespace(ns migration.Namespace, fsys fs.FS) error {
	if err := ns.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, ErrAdapterPGMigrate,
			"postgres: invalid migration namespace", err)
	}
	if fsys == nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
			"postgres: migration set entry requires a non-nil fs.FS",
			errcode.WithDetails(errcode.PublicString("namespace", ns.String())))
	}
	if _, dup := s.seen[ns]; dup {
		return errcode.New(errcode.KindInvalid, ErrAdapterPGMigrate,
			"postgres: duplicate migration namespace",
			errcode.WithDetails(errcode.PublicString("namespace", ns.String())))
	}
	s.seen[ns] = struct{}{}
	s.entries = append(s.entries, migrationSetEntry{ns: ns, fsys: fsys})
	return nil
}

// Namespaces returns the registered namespaces in registration (application)
// order.
func (s *MigrationSet) Namespaces() []migration.Namespace {
	out := make([]migration.Namespace, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.ns
	}
	return out
}

// ApplyAll applies every namespace's pending migrations in registration order,
// each against its own schema_migrations_<namespace> tracking table. It uses
// plain Up per namespace; a populated-table forward-rebuild stays a
// platform-only break-glass operation via Migrator.ForwardRebuild, and Up's
// fail-closed gate still refuses an unpermitted populated rebuild. Idempotent:
// already-applied namespaces are no-ops.
func (s *MigrationSet) ApplyAll(ctx context.Context, pool *Pool) error {
	for _, e := range s.entries {
		if err := applyNamespace(ctx, pool, e); err != nil {
			return err
		}
	}
	return nil
}

// applyNamespace opens a Migrator for one namespace, runs Up, and closes it.
// Extracted so ApplyAll stays within the cognitive-complexity budget and the
// per-namespace *sql.DB is always released (Close error surfaces only when Up
// succeeded).
func applyNamespace(ctx context.Context, pool *Pool, e migrationSetEntry) (retErr error) {
	m, err := NewMigrator(pool, e.fsys, e.ns)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := m.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()
	if err := m.Up(ctx); err != nil {
		return fmt.Errorf("postgres: apply migrations for namespace %q: %w", e.ns, err)
	}
	return nil
}

// VerifyAll checks each namespace's database version against the max version in
// its embedded migration FS (see VerifyExpectedVersion). Used by the schema
// guard on startup paths that verify (not apply) migrations.
func (s *MigrationSet) VerifyAll(ctx context.Context, pool *Pool) error {
	for _, e := range s.entries {
		if err := VerifyExpectedVersion(ctx, pool, e.fsys, e.ns); err != nil {
			return err
		}
	}
	return nil
}
