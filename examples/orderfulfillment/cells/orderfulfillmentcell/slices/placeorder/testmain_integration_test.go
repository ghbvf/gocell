//go:build integration

// Package placeorder_test: TestMain manages the shared PostgreSQL container
// lifecycle for integration tests in this package.
//
// sharedPG is a pgtest.Shared backed by a template DB that already has the
// platform schema applied (saga_journal, projection_checkpoints, …). Each
// integration test that needs a real PG DB calls sharedPG.NewPerTestPool(t)
// to get an isolated per-test pool, then applies the orderfulfillment-specific
// migration (order_saga_status table) before wiring the durable stack.
package placeorder_test

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/adapters/postgres/pgtest"
)

// sharedPG owns the one container + pre-migrated template for this test binary.
// pgtest.New applies the platform migrations (saga_journal, projection_events,
// projection_checkpoints, …) to the template DB once; per-test clones inherit
// those tables. The orderfulfillment-specific migration (order_saga_status) is
// applied by each integration test individually after calling NewPerTestPool,
// because it belongs to the example's own migration namespace, not the
// platform namespace.
var sharedPG = pgtest.New("gocell_orderfulfillment_durable_replay_template")

// TestMain tears down the shared container after all tests have run.
// Guarded by the "integration" build tag so `go test ./...` (without the tag)
// skips this file entirely and the package does not require Docker.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
