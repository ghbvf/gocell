//go:build integration

package postgres

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/adapters/postgres/pgtest"
)

// Package-shared PostgreSQL lifecycle: one container per test binary; per-test
// isolation via `CREATE DATABASE <test> TEMPLATE <pre-migrated>` clone. See
// adapters/postgres/pgtest (mirrors configcore's testmain).
var sharedPG = pgtest.New("gocell_registrycore_repo_test_template")

// TestMain owns shared-container teardown.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
