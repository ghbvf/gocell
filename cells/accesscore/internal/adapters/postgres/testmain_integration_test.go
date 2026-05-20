//go:build integration

package postgres

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil/pgshare"
)

// Package-shared PostgreSQL lifecycle: one container per test binary;
// per-test isolation via `CREATE DATABASE <test> TEMPLATE <pre-migrated>`
// clone. See tests/testutil/pgshare for the mechanics.
//
// accesscore PG repository tests previously paid ~7 × container cold start
// across user_repo / role_repo integration tests; the shared template
// collapses that onto one boot + N × ~100ms file clones.
var sharedPG = pgshare.New("gocell_accesscore_repo_test_template")

// TestMain owns shared-container teardown. Runs once per test binary
// after all tests in the package complete.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
