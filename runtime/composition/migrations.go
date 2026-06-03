package composition

import (
	"fmt"
	"io/fs"

	"github.com/ghbvf/gocell/pkg/migration"
)

// MigrationRegistration is a layer-neutral (namespace, fs.FS) migration source
// registered via [Builder.WithMigrations]. composition does NOT execute
// migrations — runtime/ must not depend on adapters/, and migrations run
// out-of-band BEFORE Build brings up cells (cells need their schema first).
// The registrations are surfaced via [Builder.Migrations] for an ops /
// composition-root entry point (which may import adapters/postgres) to drain
// into an adapters/postgres.MigrationSet and apply against a real database.
//
// ref: docs/guides/cell-external-repo-quickstart.md "Migrations" — the
// execution-bridge loop external Cell modules write.
type MigrationRegistration struct {
	// Namespace identifies the migration lineage; its tracking table is
	// schema_migrations_<namespace> (derived adapter-side). The reserved
	// "platform" namespace is the platform's own migrations and is seeded by the
	// ops entry point (adapters/postgres.NewMigrationSetWithPlatform), not here.
	Namespace migration.Namespace
	// FS is the embed.FS (or any fs.FS) of goose-native NNN_desc.sql files for
	// this namespace, with its own independent 001..N sequence.
	FS fs.FS
}

// WithMigrations registers an external Cell module's migration set under ns.
// Calls accumulate (successive calls append). The registration is validated at
// [Builder.Build] (namespace validity, non-nil FS, no duplicate namespace);
// Build does not execute the migrations.
func (b *Builder) WithMigrations(ns migration.Namespace, fsys fs.FS) *Builder {
	b.migrations = append(b.migrations, MigrationRegistration{Namespace: ns, FS: fsys})
	return b
}

// Migrations returns the registered migration sets in registration order, for
// the ops / composition-root entry point to apply before Build. Returns a copy
// so callers cannot mutate the builder's backing slice.
func (b *Builder) Migrations() []MigrationRegistration {
	if len(b.migrations) == 0 {
		return nil
	}
	out := make([]MigrationRegistration, len(b.migrations))
	copy(out, b.migrations)
	return out
}

// validateMigrations checks the accumulated migration registrations are
// well-formed: each namespace is valid, each FS is non-nil, and no namespace is
// registered twice. It runs at Build time (before any module Provide). It does
// NOT execute migrations — authoritative reserved-namespace / table derivation
// lives in adapters/postgres.MigrationSet.Add at apply time.
func (b *Builder) validateMigrations() error {
	seen := make(map[migration.Namespace]struct{}, len(b.migrations))
	for _, r := range b.migrations {
		if err := r.Namespace.Validate(); err != nil {
			return fmt.Errorf("composition.Builder.Build: invalid migration namespace: %w", err)
		}
		if r.FS == nil {
			return fmt.Errorf("composition.Builder.Build: WithMigrations(%q) requires a non-nil fs.FS",
				r.Namespace)
		}
		if _, dup := seen[r.Namespace]; dup {
			return fmt.Errorf("composition.Builder.Build: duplicate migration namespace %q", r.Namespace)
		}
		seen[r.Namespace] = struct{}{}
	}
	return nil
}
