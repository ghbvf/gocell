# Integration Testing Guide

> GoCell integration testing strategy: build tags, infrastructure setup, and journey verification.

## Overview

GoCell uses a two-tier testing strategy:

1. **Unit tests** (`go test ./...`) -- run without external dependencies, cover kernel logic, type validation, and handler stubs via `httptest`.
2. **Integration tests** (`go test -tags=integration ./...`) -- require running infrastructure (PostgreSQL, Redis, RabbitMQ, etc.) and exercise full adapter chains, cross-cell journeys, and assembly lifecycle.

## Build Tag Convention

All integration test files use the `integration` build tag:

```go
//go:build integration

package postgres

import "testing"

func TestIntegration_PingConnection(t *testing.T) {
    // ...
}
```

This ensures `go test ./...` (the default CI gate) never attempts to connect to external services.

## Test File Locations

| Category | Path | Purpose |
|----------|------|---------|
| Adapter integration | `adapters/{name}/integration_test.go` | Per-adapter connectivity and operations |
| Journey integration | `tests/integration/journey_test.go` | End-to-end cross-cell journeys |
| Assembly integration | `tests/integration/assembly_test.go` | Assembly startup, shutdown, and wiring |
| Handler httptest | `runtime/http/handler_test.go` | HTTP handler layer (no build tag needed) |

## Infrastructure Setup

### Docker Compose (recommended)

Create a `docker-compose.test.yml` at the project root:

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: gocell_test
      POSTGRES_USER: test
      POSTGRES_PASSWORD: test
    ports: ["5432:5432"]

  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]

  rabbitmq:
    image: rabbitmq:3-management-alpine
    ports: ["5672:5672", "15672:15672"]

  minio:
    image: minio/minio
    command: server /data
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    ports: ["9000:9000"]
```

### Running Integration Tests

```bash
# Start infrastructure
docker compose -f docker-compose.test.yml up -d

# Wait for services to be ready
sleep 5

# Run integration tests
go test -tags=integration -count=1 -timeout=120s ./...

# Tear down
docker compose -f docker-compose.test.yml down -v
```

### Environment Variables

Integration tests read connection strings from environment variables:

| Variable | Default | Example |
|----------|---------|---------|
| `GOCELL_POSTGRES_DSN` | `postgres://test:test@localhost:5432/gocell_test?sslmode=disable` | |
| `GOCELL_REDIS_ADDR` | `localhost:6379` | |
| `GOCELL_RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | |
| `GOCELL_S3_ENDPOINT` | `http://localhost:9000` | |
| `GOCELL_OIDC_ISSUER` | `http://localhost:8080/realms/gocell` | |

## Writing Integration Tests

### Adapter Tests

Each adapter package has an `integration_test.go` that tests:

1. **Connectivity** -- ping/connect to the service
2. **CRUD operations** -- basic read/write round trips
3. **Error handling** -- connection failures, timeouts
4. **Close** -- graceful shutdown releases resources

Example pattern:

```go
//go:build integration

package postgres

import (
    "context"
    "os"
    "testing"
    "time"
)

func dsn() string {
    if v := os.Getenv("GOCELL_POSTGRES_DSN"); v != "" {
        return v
    }
    return "postgres://test:test@localhost:5432/gocell_test?sslmode=disable"
}

func TestIntegration_PingConnection(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // adapter := NewPostgresAdapter(dsn())
    // defer adapter.Close()
    // err := adapter.Ping(ctx)
    // require.NoError(t, err)
    _ = ctx
    t.Skip("stub: implementation pending")
}
```

### Journey Tests

Journey tests verify end-to-end user workflows as defined in `journeys/J-*.yaml`.
Each journey test follows the steps declared in the YAML spec:

```go
func TestJourney_AuditLoginTrail(t *testing.T) {
    // Step 1: User authenticates via access-core (session-login)
    // Step 2: session.created event is published
    // Step 3: audit-core (audit-append) consumes the event
    // Step 4: audit-core (audit-query) returns the audit trail
}
```

### Assembly Tests

Assembly tests verify that the full core-bundle (access-core + audit-core + config-core) starts, runs, and stops correctly:

```go
func TestAssembly_CoreBundleStartStop(t *testing.T) {
    // Register all three cells
    // Start assembly
    // Check all cells report healthy
    // Stop assembly
    // Verify clean shutdown
}
```

## Outbox Pattern Testing (T71)

The outbox full-chain test in `adapters/postgres/integration_test.go` verifies:

1. Business row + outbox entry written in a single transaction
2. Outbox poller reads the pending entry
3. Event is published to the message broker
4. Outbox entry is marked as delivered
5. On failure, both business row and outbox entry are rolled back

## DLQ Testing (T72)

The dead-letter queue test in `adapters/rabbitmq/integration_test.go` verifies:

1. A message is published and consumed
2. The consumer handler returns an error (simulating failure)
3. The message is retried up to MaxRetries
4. After exhausting retries, the message is routed to the DLQ
5. The DLQ message is readable for manual inspection

## CI Integration

The CI pipeline runs two stages:

1. **Unit tests** (always): `go test -count=1 ./...`
2. **Integration tests** (on merge to develop): `go test -tags=integration -count=1 -timeout=120s ./...`

The `go test ./... -count=1` command (T76) must pass with zero failures at all times.
