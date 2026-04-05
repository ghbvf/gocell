# Integration Testing Guide

This guide explains how to write and run integration tests for GoCell, covering
Docker Compose service setup, the `make test-integration` target, testcontainers
usage for local development, and environment variable configuration.

---

## Prerequisites

- Go 1.25+
- Docker Engine 24+ with Compose v2 (for `docker compose` command)
- `make` available on PATH

---

## Service Architecture for Tests

GoCell integration tests require the following external services:

| Service    | Image                       | Default Port | Purpose                              |
|------------|----------------------------|--------------|--------------------------------------|
| PostgreSQL | `postgres:16-alpine`        | 5432         | Cell repository persistence          |
| Redis      | `redis:7-alpine`            | 6379         | Rate limiting, idempotency keys      |
| RabbitMQ   | `rabbitmq:3.13-management`  | 5672 / 15672 | Durable event delivery (adapters/)   |
| MinIO      | `minio/minio:latest`        | 9000 / 9001  | S3-compatible audit archive storage  |

> Phase 2 cells use in-memory stubs; the above services are only required when
> testing Phase 3 adapters (adapters/postgres, adapters/redis, adapters/rabbitmq,
> adapters/s3).

---

## Running Integration Tests

### Using Docker Compose

A `docker-compose.test.yml` file (Phase 3 deliverable) will bring up required
services. For now, start services manually or use testcontainers (see below).

```bash
# Start test services
docker compose -f docker-compose.test.yml up -d

# Run integration tests (build tag gates them from the normal test suite)
make test-integration

# Tear down
docker compose -f docker-compose.test.yml down -v
```

### Makefile Target

```makefile
test-integration:
	go test ./... -tags integration -count=1 -timeout 120s
```

The `-tags integration` build tag separates integration tests from unit tests.
Normal `make test` (no tag) only runs unit tests and does not require Docker.

### Running a Single Package

```bash
go test -tags integration -v ./adapters/postgres/... -run TestUserRepository
```

---

## Writing Integration Tests

### Build Tag Convention

Place the build constraint at the top of every integration test file:

```go
//go:build integration

package postgres_test
```

### Test Setup Pattern

```go
//go:build integration

package postgres_test

import (
    "context"
    "os"
    "testing"

    "github.com/ghbvf/gocell/adapters/postgres"
)

func TestMain(m *testing.M) {
    // Requires POSTGRES_HOST (or testcontainers — see below).
    os.Exit(m.Run())
}

func TestUserRepository_Create(t *testing.T) {
    cfg := postgres.ConfigFromEnv()
    pool, err := postgres.New(context.Background(), cfg)
    if err != nil {
        t.Fatalf("connect postgres: %v", err)
    }
    defer pool.Close()

    // ... test logic
}
```

---

## Testcontainers for Local Development

When Docker is available locally but no Compose file is set up, use
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) to
start throwaway containers inside the test binary.

### Install

```bash
go get github.com/testcontainers/testcontainers-go
```

### PostgreSQL Example

```go
//go:build integration

package postgres_test

import (
    "context"
    "fmt"
    "testing"

    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"

    "github.com/ghbvf/gocell/adapters/postgres"
)

func startPostgres(t *testing.T) postgres.Config {
    t.Helper()
    ctx := context.Background()

    req := testcontainers.ContainerRequest{
        Image:        "postgres:16-alpine",
        ExposedPorts: []string{"5432/tcp"},
        Env: map[string]string{
            "POSTGRES_USER":     "test",
            "POSTGRES_PASSWORD": "test",
            "POSTGRES_DB":       "testdb",
        },
        WaitingFor: wait.ForListeningPort("5432/tcp"),
    }

    container, err := testcontainers.GenericContainer(ctx,
        testcontainers.GenericContainerRequest{
            ContainerRequest: req,
            Started:          true,
        },
    )
    if err != nil {
        t.Fatalf("start postgres container: %v", err)
    }
    t.Cleanup(func() { _ = container.Terminate(ctx) })

    host, _ := container.Host(ctx)
    port, _ := container.MappedPort(ctx, "5432")

    return postgres.Config{
        Host:     host,
        Port:     port.Int(),
        User:     "test",
        Password: "test",
        DBName:   "testdb",
        SSLMode:  "disable",
    }
}
```

### Redis Example

```go
func startRedis(t *testing.T) string {
    t.Helper()
    ctx := context.Background()

    req := testcontainers.ContainerRequest{
        Image:        "redis:7-alpine",
        ExposedPorts: []string{"6379/tcp"},
        WaitingFor:   wait.ForListeningPort("6379/tcp"),
    }
    container, err := testcontainers.GenericContainer(ctx,
        testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true},
    )
    if err != nil {
        t.Fatalf("start redis container: %v", err)
    }
    t.Cleanup(func() { _ = container.Terminate(ctx) })

    host, _ := container.Host(ctx)
    port, _ := container.MappedPort(ctx, "6379")
    return fmt.Sprintf("%s:%s", host, port.Port())
}
```

---

## Environment Variable Configuration

Integration tests read configuration from environment variables (same as
production). Set variables in your shell, in a `.env` file (not committed),
or via `docker compose --env-file`.

For the full variable reference, see
[docs/guides/adapter-config-reference.md](./adapter-config-reference.md).

### Minimal Local Set (PostgreSQL + Redis)

```bash
# PostgreSQL
export POSTGRES_HOST=localhost
export POSTGRES_PORT=5432
export POSTGRES_USER=gocell
export POSTGRES_PASSWORD=secret
export POSTGRES_DB=gocell_test
export POSTGRES_SSLMODE=disable

# Redis
export REDIS_ADDR=localhost:6379
export REDIS_PASSWORD=
export REDIS_DB=1
```

### Loading from .env.example

Copy `src/.env.example` (Phase 3 deliverable) and override values:

```bash
cp src/.env.example src/.env.test
# Edit src/.env.test with test credentials
set -a && source src/.env.test && set +a
make test-integration
```

---

## CI Pipeline

The GitHub Actions workflow (`.github/workflows/ci.yml`, Phase 3 deliverable)
runs integration tests in a service container environment:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    env:
      POSTGRES_USER: gocell
      POSTGRES_PASSWORD: secret
      POSTGRES_DB: gocell_test
    options: >-
      --health-cmd pg_isready
      --health-interval 5s
      --health-timeout 5s
      --health-retries 5

  redis:
    image: redis:7-alpine
    options: >-
      --health-cmd "redis-cli ping"
      --health-interval 5s
      --health-timeout 5s
      --health-retries 5
```

Unit tests (`make test`) run on every PR. Integration tests
(`make test-integration`) run on merge to `develop` and before release.

---

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| `connection refused :5432` | Postgres not started | `docker compose up -d postgres` |
| `dial tcp: connection refused :6379` | Redis not started | `docker compose up -d redis` |
| `container start timeout` | Docker daemon slow | Increase testcontainers timeout or retry |
| Tests skipped without `-tags integration` | Build tag absent | Add `//go:build integration` to test file |
| `go: no packages loaded` | Wrong working directory | Run from `src/` directory |
