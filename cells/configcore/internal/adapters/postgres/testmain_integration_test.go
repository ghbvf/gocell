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
// Wall-time impact: the package's six setup-helper call sites previously
// paid ~6 × (~7s container cold start + ~3s migrations); the shared
// template collapses that to one container + one migration + per-test
// ~100ms file clone.
var sharedPG = pgshare.New("gocell_configcore_repo_test_template")

// TestMain owns shared-container teardown. Runs once per test binary
// after all tests in the package complete.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}

