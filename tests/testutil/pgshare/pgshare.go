//go:build integration

// Package pgshare provides a process-wide shared PostgreSQL container with
// pre-migrated template-DB clone semantics for integration test packages that
// can import adapters/postgres.
//
// pgshare is a thin adapterpg-typed wrapper over tests/testutil/pgclone:
// pgclone owns the container + template + clone lifecycle (and is the single
// sanctioned holder of tcpostgres.Run); pgshare supplies the adapters/postgres
// migration callback and returns a typed *adapterpg.Pool. The public API
// (New / NewPerTestPool / Shutdown) is unchanged from the pre-pgclone form, so
// the cells packages that adopted pgshare need no edits.
//
// Packages whose tests are white-box `package postgres` — i.e. adapters/postgres
// itself — cannot import pgshare: it imports adapterpg, which would form an
// import cycle. Those packages import pgclone directly with their own
// in-package migration callback. See pgclone's package doc.
package pgshare

import (
	"context"
	"fmt"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/tests/testutil/pgclone"
)

// Shared owns the per-package container + template DB lifecycle via an
// embedded *pgclone.Shared. Create one in a package-scope var and call
// Shutdown from TestMain.
//
// The zero value is invalid; callers must use New.
type Shared struct {
	inner *pgclone.Shared
}

// New returns a *Shared bound to templateDB (Postgres identifier rules — see
// pgclone.New). Use a unique name per Go test binary
// (`gocell_<pkg>_test_template` is the convention).
//
// The instance is dormant until the first NewPerTestPool call, which boots the
// container and applies the adapters/postgres migration set to the template
// once.
func New(templateDB string) *Shared {
	return &Shared{inner: pgclone.New(templateDB, applyMigrations)}
}

// applyMigrations runs the production adapters/postgres migration set against
// the template DSN. It closes the pool before returning per the
// pgclone.MigrateFunc contract: CREATE DATABASE ... TEMPLATE rejects a source
// DB with active connections, so the pool MUST be closed before the first
// clone (the goose *sql.DB opened by NewMigrator is released transitively by
// pool.Close — the historic applyMigrationsToTemplate behaviour this preserves).
func applyMigrations(ctx context.Context, templateDSN string) (err error) {
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: templateDSN})
	if err != nil {
		return fmt.Errorf("open template pool: %w", err)
	}
	defer func() {
		// A close error matters here: a lingering connection on the template
		// blocks the first CREATE DATABASE ... TEMPLATE clone. Surface it (when
		// migration itself succeeded) so boot fails fast with a clear cause
		// instead of the first CloneDSN failing opaquely with "source DB in use".
		if cerr := pool.Close(ctx); cerr != nil && err == nil {
			err = fmt.Errorf("close migration pool: %w", cerr)
		}
	}()

	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("load migrations fs: %w", err)
	}
	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, migration.PlatformNamespace)
	if err != nil {
		return fmt.Errorf("new migrator: %w", err)
	}
	if err := migrator.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// NewPerTestPool clones the shared template database into a fresh per-test
// database, opens a Pool against it, and registers cleanup via t.Cleanup. The
// returned pool's lifetime is bound to the test; callers must NOT call
// pool.Close manually.
//
// First call also boots the container and applies migrations (one-shot for the
// test binary). Subsequent calls pay only the file-clone cost (~50-200ms).
func (s *Shared) NewPerTestPool(t *testing.T) *adapterpg.Pool {
	t.Helper()
	dsn := s.inner.CloneDSN(t)
	pool, err := adapterpg.NewPool(context.Background(), adapterpg.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open pool to per-test DB: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Close(context.Background()); err != nil {
			t.Logf("WARN: per-test pool close: %v", err)
		}
	})
	return pool
}

// Shutdown terminates the shared container if it was booted. Safe to call even
// when the singleton was never initialized (no-op). Call from TestMain after
// m.Run.
func (s *Shared) Shutdown() {
	s.inner.Shutdown()
}
