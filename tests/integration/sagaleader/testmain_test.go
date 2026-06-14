//go:build integration

package sagaleader

import (
	"context"
	"fmt"
	"os"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/tests/testutil/pgclone"
)

// sharedPG owns one PostgreSQL container for the whole sagaleader test binary.
// The migrated template carries the adapters/postgres migration set (incl.
// 040_create_saga_tables); per-test databases are file-clones.
//
// pgclone is the single sanctioned holder of tcpostgres.Run
// (PG-TESTCONTAINER-FUNNEL-01); sagaleader is package-external under tests/ so
// it may import adapters/postgres directly with an in-package MigrateFunc (same
// shape as l2atomicity / adapters/postgres TestMain).
var sharedPG = pgclone.New("gocell_sagaleader_test_template",
	func(ctx context.Context, dsn string) (err error) {
		pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
		if err != nil {
			return fmt.Errorf("open template pool: %w", err)
		}
		defer func() {
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
		return nil
	})

// TestMain owns shared-container teardown.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
