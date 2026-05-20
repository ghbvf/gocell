//go:build integration

package configwrite

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil/pgshare"
)

// Package-shared PostgreSQL lifecycle: one container per test binary;
// per-test isolation via `CREATE DATABASE <test> TEMPLATE <pre-migrated>`
// clone. See tests/testutil/pgshare for the mechanics.
//
// configwrite has four PG-backed integration tests (Create_AtomicWithOutbox,
// Update_AtomicWithOutbox, Delete_AtomicWithOutbox,
// Create_RollbackOnOutboxFailure); the shared template collapses ~4 ×
// container cold starts onto one boot + four ~100ms file clones.
var sharedPG = pgshare.New("gocell_configwrite_test_template")

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}

