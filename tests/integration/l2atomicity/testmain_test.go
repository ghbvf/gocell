//go:build integration

package l2atomicity

import (
	"context"
	"fmt"
	"os"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/migration"
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
		migrator, err := adapterpg.NewMigrator(pool, fsys, migration.PlatformNamespace)
		if err != nil {
			return fmt.Errorf("new migrator: %w", err)
		}
		if err := migrator.Up(ctx); err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if err := adapterpg.VerifyExpectedShape(ctx, pool); err != nil {
			return fmt.Errorf("verify schema shape: %w", err)
		}
		// Migration 047 rebuilt roles with composite PK (tenant_id, id). Seed the
		// "editor" role for a fixed test-tenant so RBAC cascade tests can
		// assign/revoke it without FK violations. ON CONFLICT now targets the
		// composite PK; the single-column (id) constraint no longer exists.
		if _, err := pool.DB().Exec(ctx,
			`INSERT INTO roles (tenant_id, id, name)
			 VALUES ('00000000-0000-0000-0000-000000000001', 'editor', 'editor')
			 ON CONFLICT (tenant_id, id) DO NOTHING`); err != nil {
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
