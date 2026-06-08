//go:build integration

// Package pgtest provides adapter-owned PostgreSQL test helpers for packages
// that are allowed to depend on adapters/postgres.
package pgtest

import (
	"context"
	"fmt"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/tests/testutil/pgclone"
)

// Shared owns the per-package container + template DB lifecycle via pgclone.
type Shared struct {
	inner *pgclone.Shared
}

// New returns a *Shared bound to templateDB.
func New(templateDB string) *Shared {
	return &Shared{inner: pgclone.New(templateDB, applyMigrations)}
}

func applyMigrations(ctx context.Context, templateDSN string) (err error) {
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: templateDSN})
	if err != nil {
		return fmt.Errorf("open template pool: %w", err)
	}
	defer func() {
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

// NewPerTestPool clones the shared template DB and opens an adapter Pool.
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

// Shutdown terminates the shared container if it was booted.
func (s *Shared) Shutdown() {
	s.inner.Shutdown()
}
