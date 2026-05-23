//go:build integration

package l2atomicity

import (
	"context"
	"fmt"
	"os"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/tests/testutil/pgclone"
)

// sharedPG owns one PostgreSQL container for the whole l2atomicity test binary.
// The migrated template carries the adapters/postgres migration set plus the
// "editor" role row that RBAC cascade tests assign/revoke; per-test databases
// are cloned (~10-200ms each), replacing the historic per-test container
// spin-up + 32-migration replay (~20s/test, ~222s total) that dominated the
// race-pg-integration lane.
//
// l2atomicity can import adapters/postgres, so it uses pgclone directly with an
// in-package MigrateFunc (the same shape as adapters/postgres's TestMain).
// pgclone is the single sanctioned holder of tcpostgres.Run
// (PG-TESTCONTAINER-FUNNEL-01).
var sharedPG = pgclone.New("gocell_l2atomicity_test_template",
	func(ctx context.Context, dsn string) (err error) {
		pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
		if err != nil {
			return fmt.Errorf("open template pool: %w", err)
		}
		defer func() {
			// Close error matters: a lingering connection on the template blocks
			// the first CREATE DATABASE ... TEMPLATE clone. Surface it (when the
			// migration itself succeeded) so boot fails fast with a clear cause.
			if cerr := pool.Close(ctx); cerr != nil && err == nil {
				err = fmt.Errorf("close migration pool: %w", cerr)
			}
		}()
		fsys, err := adapterpg.MigrationsFS()
		if err != nil {
			return fmt.Errorf("load migrations fs: %w", err)
		}
		migrator, err := adapterpg.NewMigrator(pool, fsys, "schema_migrations")
		if err != nil {
			return fmt.Errorf("new migrator: %w", err)
		}
		if err := migrator.Up(ctx); err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if err := adapterpg.VerifyExpectedShape(ctx, pool); err != nil {
			return fmt.Errorf("verify schema shape: %w", err)
		}
		// Migration 019 creates the `roles` table but only the admin role is
		// seeded by the setup flow. RBAC cascade tests assign/revoke a non-admin
		// role, which requires the row to exist or AssignToUser fails with an FK
		// violation. Seed "editor" once into the template; every clone inherits it.
		if _, err := pool.DB().Exec(ctx,
			`INSERT INTO roles (id, name) VALUES ('editor', 'editor') ON CONFLICT (id) DO NOTHING`); err != nil {
			return fmt.Errorf("seed editor role: %w", err)
		}
		return nil
	})

// TestMain owns shared-container teardown. Runs once per test binary after all
// tests complete.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
