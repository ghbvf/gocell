# Integration Testing Guide

## Overview

GoCell integration tests verify adapter behaviour against real infrastructure (PostgreSQL, Redis, RabbitMQ, MinIO, Keycloak) and end-to-end journey scenarios with a fully assembled core-bundle.

All integration test files use the `//go:build integration` build tag so they are excluded from the default `go test ./...` run.

## Prerequisites

```bash
docker compose up -d          # boots all infra services
docker compose ps             # verify all services are healthy
```

The `docker-compose.yml` at the repository root defines the required services.

## Running Integration Tests

### All integration tests

```bash
cd src
go test -tags integration ./... -count=1 -v
```

### Single adapter

```bash
go test -tags integration ./adapters/postgres/... -count=1 -v
go test -tags integration ./adapters/redis/...    -count=1 -v
go test -tags integration ./adapters/rabbitmq/... -count=1 -v
go test -tags integration ./adapters/oidc/...     -count=1 -v
go test -tags integration ./adapters/s3/...       -count=1 -v
go test -tags integration ./adapters/websocket/.. -count=1 -v
```

### Journey tests only

```bash
go test -tags integration ./tests/integration/... -run TestJourney -count=1 -v
```

### Assembly tests only

```bash
go test -tags integration ./tests/integration/... -run TestAssembly -count=1 -v
```

## Test File Conventions

| Location | Purpose |
|----------|---------|
| `adapters/{name}/integration_test.go` | Adapter-level tests against real infrastructure |
| `tests/integration/journey_test.go` | Cross-cell journey scenarios (J-*) |
| `tests/integration/assembly_test.go` | Assembly boot, shutdown, and isolation tests |

## Writing a New Integration Test

1. Add `//go:build integration` as the first line (before `package`).
2. Use `t.Skip("stub: requires ...")` for placeholder tests until the infrastructure helper is ready.
3. Read connection parameters from environment variables (e.g., `GOCELL_PG_DSN`, `GOCELL_REDIS_ADDR`).
4. Each test must be self-contained: create its own schema/queue/bucket, run assertions, then clean up.
5. Use `t.Parallel()` only when tests do not share mutable state (e.g., separate database schemas).

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GOCELL_PG_DSN` | `postgres://gocell:gocell@localhost:5432/gocell_test?sslmode=disable` | PostgreSQL connection string |
| `GOCELL_REDIS_ADDR` | `localhost:6379` | Redis address |
| `GOCELL_AMQP_URL` | `amqp://guest:guest@localhost:5672/` | RabbitMQ connection URL |
| `GOCELL_S3_ENDPOINT` | `http://localhost:9000` | MinIO / S3 endpoint |
| `GOCELL_S3_ACCESS_KEY` | `minioadmin` | S3 access key |
| `GOCELL_S3_SECRET_KEY` | `minioadmin` | S3 secret key |
| `GOCELL_OIDC_ISSUER` | `http://localhost:8080/realms/gocell` | OIDC issuer URL |

## CI Pipeline

Integration tests run in a separate CI stage after unit tests pass. The pipeline:

1. Boots Docker Compose services via `docker compose up -d --wait`.
2. Runs `go test -tags integration ./... -count=1`.
3. Tears down services via `docker compose down`.

See `scripts/healthcheck-verify.sh` for the health-check gate that precedes integration tests.
