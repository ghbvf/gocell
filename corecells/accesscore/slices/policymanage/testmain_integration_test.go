//go:build integration

package policymanage

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/adapters/postgres/pgtest"
)

// Package-shared PostgreSQL lifecycle: one container per test binary; per-test
// isolation via `CREATE DATABASE <test> TEMPLATE <pre-migrated>` clone. The
// template is migrated once with the full platform migration set (the same set
// the per-test setup applied manually before #2116). See adapters/postgres/pgtest
// for the mechanics; routing through this funnel satisfies PG-TESTCONTAINER-FUNNEL-01.
var sharedPG = pgtest.New("gocell_policymanage_repo_test_template")

// TestMain owns shared-container teardown. Runs once per test binary after all
// tests in the package complete.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
