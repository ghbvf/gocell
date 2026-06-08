//go:build integration

package configpublish

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/adapters/postgres/pgtest"
)

// Package-shared PostgreSQL lifecycle: one container per test binary;
// per-test isolation via `CREATE DATABASE <test> TEMPLATE <pre-migrated>`
// clone. See adapters/postgres/pgtest for the mechanics.
//
// configpublish has multiple PG-backed integration tests; the shared
// template collapses N × container cold starts onto one boot + N × ~100ms
// file clones.
var sharedPG = pgtest.New("gocell_configpublish_test_template")

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
