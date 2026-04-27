# Integration Testing Guide

## Overview

GoCell integration tests verify adapter behaviour against real infrastructure (PostgreSQL, Redis, RabbitMQ, MinIO, Keycloak) and end-to-end journey scenarios with a fully assembled corebundle.

All integration test files use the `//go:build integration` build tag so they are excluded from the default `go test ./...` run.

## Prerequisites

Start the local Docker daemon. Integration tests use testcontainers and
self-provision PostgreSQL, Redis, RabbitMQ, Vault, and similar dependencies per
test. Do not start the repository root `docker-compose.yml` for the
testcontainer-backed integration suite.

Use strict mode when a run must fail if Docker is unavailable:

```bash
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./adapters/postgres/... -count=1
```

## Running Integration Tests

### All integration tests

```bash
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags=integration,e2e \
  ./adapters/... \
  ./tests/integration/... \
  ./tests/e2e/internal/... \
  ./cmd/corebundle/... \
  ./examples/ssobff/... \
  ./cells/accesscore/slices/identitymanage/... \
  ./runtime/bootstrap/... \
  -count=1 -timeout 15m -v
```

### Single adapter

```bash
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./adapters/postgres/... -count=1 -v
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./adapters/redis/...    -count=1 -v
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./adapters/rabbitmq/... -count=1 -v
go test -tags integration ./adapters/websocket/... -count=1 -v
# adapters/oidc and adapters/s3 are thin SDK wrappers with unit tests only.
```

### Journey tests only

```bash
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./tests/integration/... -run TestJourney -count=1 -v
```

### Assembly tests only

```bash
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./tests/integration/... -run TestAssembly -count=1 -v
```

### OTel collector protocol test

PR and push CI run the minimal real OpenTelemetry Collector round-trip smoke.
Run the same check locally with:

```bash
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags=integration,otelcollector ./adapters/otel/... \
  -run '^TestNewTracer_ExportsSpanToOTLPCollector$' -count=1 -timeout 10m -v
```

The nightly/manual workflow runs the full `./adapters/otel/...` package under
the same tags as a supplemental compatibility patrol.

## Test File Conventions

| Location | Purpose |
|----------|---------|
| `adapters/{name}/integration_test.go` | Adapter-level tests against real infrastructure |
| `tests/integration/journey_test.go` | Cross-cell journey scenarios (J-*) |
| `tests/integration/assembly_test.go` | Assembly boot, shutdown, and isolation tests |

## Writing a New Integration Test

1. Add `//go:build integration` as the first line (before `package`).
2. If the test starts any testcontainer, call `testutil.RequireDocker(t)` before
   `testcontainers.GenericContainer` or any `testcontainers-go/modules/* Run`
   call:

   ```go
   func setupPostgres(t *testing.T) {
       t.Helper()
       testutil.RequireDocker(t)
       container, err := tcpostgres.Run(context.Background(), testutil.PostgresImage)
       require.NoError(t, err)
       t.Cleanup(func() { _ = container.Terminate(context.Background()) })
   }
   ```

   Local runs self-skip only when Docker is unavailable. CI and ship runs set
   `GOCELL_TEST_DOCKER_REQUIRED=1`, so Docker provider failures fail the test
   instead of producing false green.
3. Do not gate testcontainer-backed tests on DSN/address environment variables
   or `t.Skip` for missing external services. Start the container in the test
   and pass its DSN directly; see `cmd/corebundle/main_integration_test.go` for
   the pattern.
4. Each test must be self-contained: create its own schema/queue/bucket, run assertions, then clean up.
5. Use `t.Parallel()` only when tests do not share mutable state (e.g., separate database schemas).

Permanent stub tests (will never run) MUST be deleted, not marked `t.Skip` —
run `gocell check unconditional-skip ./...` to detect violations; analyzer at
`tools/nogo/unconditionalskip`.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GOCELL_TEST_DOCKER_REQUIRED` | unset | Set to `1` in CI or local ship runs to fail when the Docker provider is unhealthy. Leave unset for local self-skip when Docker is not running. |

Service DSNs such as PostgreSQL, Redis, RabbitMQ, Vault, and OTel Collector are
test-owned values returned by testcontainers. Do not require developers or CI to
preconfigure them for the integration suite.

## CI Pipeline

Integration tests run in a separate CI stage with strict Docker mode. The
pipeline:

1. Sets `GOCELL_TEST_DOCKER_REQUIRED=1`.
2. Runs the testcontainer-backed `go test -tags=integration,e2e ...` package
   set.
3. Uploads the integration coverage profile.

The OTel Collector real protocol smoke runs in PR/push CI with
`-tags=integration,otelcollector`; the nightly/manual workflow runs the broader
package under the same tags.

See `scripts/healthcheck-verify.sh` for the health-check gate that precedes integration tests.

## Cross-Platform OS Smoke Matrix

The `os-smoke` CI job runs a targeted subset of unit tests on macOS and Windows runners to
verify cross-platform behaviour that cannot be exercised on Linux alone:

```
go test -count=1 -timeout 5m \
  ./runtime/shutdown/... \
  ./cells/accesscore/initialadmin/... \
  ./runtime/config/... \
  ./kernel/governance/...
```

**When it runs:** on every PR (`pr-check.yml`) and every push to `develop` (`ci.yml`), in
parallel with the Linux `build-test` job.

**What it tests:**
- `runtime/shutdown` — platform-specific signal set (`SIGINT`/`SIGTERM` on Unix, `os.Interrupt`
  on Windows).
- `cells/accesscore/initialadmin` — per-OS credential file path resolution and DACL security
  descriptor (Windows).
- `runtime/config` — symlink pivot detection; symlink tests are skipped on Windows via
  `t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")` so the Windows runner
  passes cleanly while macOS validates the full symlink path.
- `kernel/governance` — `IsWithinRoot` symlink escape test; same Windows skip applies.

**Coverage:** the `os-smoke` job does NOT upload a coverage profile and does NOT contribute to
SonarCloud coverage. Coverage is collected exclusively from the Linux `build-test` and
`integration-test` jobs.
