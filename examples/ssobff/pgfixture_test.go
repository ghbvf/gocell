//go:build integration || examples_smoke

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ghbvf/gocell/tests/testutil"
)

// startEphemeralPostgres starts a throwaway PostgreSQL container via
// testcontainers and returns its DSN plus a cleanup func. It is the single
// DB-provisioning seam shared by the two ssobff DB-backed tests:
//
//   - TestSSOBFFStartupSmoke (//go:build examples_smoke) — passes the DSN to
//     the ssobff subprocess via the DATABASE_URL env var.
//   - TestWalkthrough (//go:build integration) — sets DATABASE_URL via
//     t.Setenv so the in-process NewSSOBFFApp picks it up.
//
// Why self-provision instead of reading host DATABASE_URL: neither CI nor the
// issue's verification command (`go test -tags=examples_smoke ./examples/ssobff
// -run TestSSOBFFStartupSmoke`) sets DATABASE_URL, so a conditional skip made
// both tests silently never run. RequireDocker moves the only skip path onto
// the repo-wide GOCELL_TEST_DOCKER_REQUIRED=1 fail-closed lever: with Docker
// the test RUNS; without Docker it self-skips locally but FAILS in CI.
//
// ssobff applies its own embedded migrations on startup (app.go migrator.Up),
// so an empty database is sufficient — no seed role/schema is pre-applied here.
//
// ref: cmd/corebundle/main_integration_test.go::setupPostgresForMain — same
// testcontainers postgres provisioning shape used across the repo's
// integration suite (ory/kratos, dapr, temporal use the same test-owns-the-
// container model).
func startEphemeralPostgres(t *testing.T) (string, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("ssobff"),
		tcpostgres.WithUsername("ssobff"),
		tcpostgres.WithPassword("ssobff"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "failed to get connection string")

	cleanup := func() {
		if terr := container.Terminate(context.Background()); terr != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", terr)
		}
	}
	return dsn, cleanup
}
