//go:build integration

package appender_test

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil/pgshare"
)

// Package-shared PostgreSQL lifecycle: one container per test binary;
// per-test isolation via CREATE DATABASE <test> TEMPLATE <pre-migrated>
// clone. See tests/testutil/pgshare for the mechanics.
//
// The appender integration tests (TestL2Atomicity_appender_RollsBack and
// TestL2Atomicity_appender_ReplayIdempotent) share this template, collapsing
// two container cold starts onto one boot + two fast file clones.
var sharedPG = pgshare.New("gocell_auditappender_test_template")

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
