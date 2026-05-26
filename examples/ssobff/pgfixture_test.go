//go:build integration || examples_smoke

package main

import (
	"context"
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil/pgclone"
)

// ssobffSharedPG owns the process-wide shared PostgreSQL container for ssobff's
// two DB-backed tests (TestSSOBFFStartupSmoke + TestWalkthrough). Routing
// through pgclone keeps tcpostgres.Run confined to the single sanctioned funnel
// holder (PG-TESTCONTAINER-FUNNEL-01) — no per-package container, no carve-out.
//
// Each test gets a fresh EMPTY per-test database (EmptyDSN); ssobff then applies
// its own embedded migrations on startup (app.go migrator.Up), faithfully
// exercising the production first-boot path.
//
// noopTemplateMigrate: pgclone.boot always builds its migrated template once,
// but EmptyDSN derives per-test DBs from template1 (not that template), so
// ssobff needs no template schema — the app self-migrates each empty DB. The
// no-op opens no connections, satisfying MigrateFunc's close-everything
// contract trivially.
var ssobffSharedPG = pgclone.New("ssobff_test_template", noopTemplateMigrate)

func noopTemplateMigrate(context.Context, string) error { return nil }

// startEphemeralPostgres returns a DSN for a fresh empty postgres database on
// the shared container. The per-test DB is dropped via t.Cleanup; the shared
// container is torn down in TestMain. pgclone boots lazily and calls
// testutil.RequireDocker first, so this self-skips locally without Docker but
// FAILS under GOCELL_TEST_DOCKER_REQUIRED=1 (the examples-smoke / integration
// CI fail-closed lever).
func startEphemeralPostgres(t *testing.T) string {
	t.Helper()
	return ssobffSharedPG.EmptyDSN(t)
}

// TestMain tears down the shared container after the DB-backed tests run. Gated
// to integration/examples_smoke so the no-tag `go test ./...` path (which runs
// the no-DB unit tests in app_test.go) is unaffected and needs no Docker.
func TestMain(m *testing.M) {
	code := m.Run()
	ssobffSharedPG.Shutdown()
	os.Exit(code)
}
