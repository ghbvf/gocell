# Adapter Configuration Reference

Each GoCell adapter reads its configuration from environment variables.
This document lists every variable, its default value, and a YAML configuration
example for use with `runtime/config` (framework startup config).

> Note: The adapter packages themselves are Phase 3 deliverables. The
> environment variables listed here match the design in
> `specs/feat/001-phase2-runtime-cells/spec.md` Appendix A and will be
> validated when adapter implementations land.

---

## PostgreSQL (`adapters/postgres`)

Used by cells/access-core, cells/audit-core, cells/config-core repository
implementations in Phase 3.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_HOST` | `localhost` | Database server hostname |
| `POSTGRES_PORT` | `5432` | Database server port |
| `POSTGRES_USER` | `gocell` | Database user |
| `POSTGRES_PASSWORD` | _(required)_ | Database password |
| `POSTGRES_DB` | `gocell` | Database name |
| `POSTGRES_SSLMODE` | `disable` | SSL mode: `disable`, `require`, `verify-full` |
| `POSTGRES_MAX_OPEN_CONNS` | `25` | Maximum open connections in pool |
| `POSTGRES_MAX_IDLE_CONNS` | `5` | Maximum idle connections in pool |
| `POSTGRES_CONN_MAX_LIFETIME` | `30m` | Maximum connection lifetime (Go duration string) |
| `POSTGRES_CONN_MAX_IDLE_TIME` | `10m` | Maximum idle connection time (Go duration string) |
| `POSTGRES_CONNECT_TIMEOUT` | `10s` | Initial connection timeout |

### YAML Example

```yaml
postgres:
  host: "localhost"
  port: 5432
  user: "gocell"
  password: "${POSTGRES_PASSWORD}"
  db: "gocell"
  sslmode: "disable"
  pool:
    maxOpenConns: 25
    maxIdleConns: 5
    connMaxLifetime: "30m"
    connMaxIdleTime: "10m"
  connectTimeout: "10s"
```

---

## Redis (`adapters/redis`)

Used for distributed rate limiting (replacing the Phase 2 in-memory token
bucket), session token caching, and idempotency key storage.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | `localhost:6379` | Redis server address (host:port) |
| `REDIS_PASSWORD` | _(empty)_ | Redis AUTH password |
| `REDIS_DB` | `0` | Redis database index (0–15) |
| `REDIS_POOL_SIZE` | `10` | Connection pool size per goroutine |
| `REDIS_MIN_IDLE_CONNS` | `2` | Minimum idle connections |
| `REDIS_DIAL_TIMEOUT` | `5s` | Connection timeout |
| `REDIS_READ_TIMEOUT` | `3s` | Read command timeout |
| `REDIS_WRITE_TIMEOUT` | `3s` | Write command timeout |
| `REDIS_IDEMPOTENCY_TTL` | `24h` | Default idempotency key TTL |

### YAML Example

```yaml
redis:
  addr: "localhost:6379"
  password: "${REDIS_PASSWORD}"
  db: 0
  pool:
    size: 10
    minIdleConns: 2
  timeouts:
    dial: "5s"
    read: "3s"
    write: "3s"
  idempotency:
    ttl: "24h"
```

---

## OIDC (`adapters/oidc`)

Used by cells/access-core session-validate slice for RS256/ES256 JWT
verification against an external identity provider.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OIDC_ISSUER_URL` | _(required)_ | OIDC provider issuer URL (discovery endpoint base) |
| `OIDC_CLIENT_ID` | _(required)_ | OAuth2 client ID for audience validation |
| `OIDC_CLIENT_SECRET` | _(empty)_ | OAuth2 client secret (only needed for token introspection) |
| `OIDC_AUDIENCE` | _(empty)_ | Expected `aud` claim; if empty, audience check is skipped |
| `OIDC_JWKS_REFRESH_INTERVAL` | `15m` | JWKS cache refresh interval |
| `OIDC_HTTP_TIMEOUT` | `10s` | HTTP timeout for OIDC discovery and JWKS requests |
| `OIDC_SKIP_ISSUER_CHECK` | `false` | Skip issuer validation (testing only) |

### YAML Example

```yaml
oidc:
  issuerUrl: "https://accounts.example.com"
  clientId: "${OIDC_CLIENT_ID}"
  clientSecret: "${OIDC_CLIENT_SECRET}"
  audience: "gocell-api"
  jwksRefreshInterval: "15m"
  httpTimeout: "10s"
```

---

## S3 (`adapters/s3`)

Used by cells/audit-core audit-archive slice for long-term audit log archival.
Compatible with AWS S3, MinIO, and any S3-protocol-compatible service.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | _(empty)_ | Custom endpoint URL (for MinIO or non-AWS S3) |
| `S3_REGION` | `us-east-1` | AWS region (or any value for MinIO) |
| `S3_BUCKET` | `gocell-audit` | Target bucket name |
| `S3_ACCESS_KEY_ID` | _(required)_ | AWS access key ID or MinIO access key |
| `S3_SECRET_ACCESS_KEY` | _(required)_ | AWS secret access key or MinIO secret key |
| `S3_SESSION_TOKEN` | _(empty)_ | AWS session token (for temporary credentials) |
| `S3_USE_PATH_STYLE` | `false` | Use path-style addressing (required for MinIO) |
| `S3_UPLOAD_TIMEOUT` | `60s` | Timeout for object upload operations |

### YAML Example (MinIO)

```yaml
s3:
  endpoint: "http://localhost:9000"
  region: "us-east-1"
  bucket: "gocell-audit"
  accessKeyId: "${S3_ACCESS_KEY_ID}"
  secretAccessKey: "${S3_SECRET_ACCESS_KEY}"
  usePathStyle: true
  uploadTimeout: "60s"
```

### YAML Example (AWS)

```yaml
s3:
  region: "ap-northeast-1"
  bucket: "my-org-gocell-audit"
  # Credentials via IAM role (no accessKeyId/secretAccessKey needed)
  uploadTimeout: "60s"
```

---

## RabbitMQ (`adapters/rabbitmq`)

Replaces runtime/eventbus.InMemoryEventBus with durable AMQP 0-9-1 delivery.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | Full AMQP connection URL |
| `RABBITMQ_VHOST` | `/` | Virtual host |
| `RABBITMQ_EXCHANGE` | `gocell.events` | Default topic exchange name |
| `RABBITMQ_PREFETCH_COUNT` | `10` | Consumer prefetch (QoS) count |
| `RABBITMQ_RECONNECT_DELAY` | `5s` | Delay between reconnection attempts |
| `RABBITMQ_RECONNECT_MAX_ATTEMPTS` | `10` | Maximum reconnection attempts before giving up |
| `RABBITMQ_PUBLISH_TIMEOUT` | `5s` | Timeout for publish operations |
| `RABBITMQ_DLX_EXCHANGE` | `gocell.deadletter` | Dead-letter exchange name |

### YAML Example

```yaml
rabbitmq:
  url: "amqp://gocell:${RABBITMQ_PASSWORD}@localhost:5672/gocell"
  vhost: "gocell"
  exchange: "gocell.events"
  prefetchCount: 10
  reconnect:
    delay: "5s"
    maxAttempts: 10
  publishTimeout: "5s"
  dlxExchange: "gocell.deadletter"
```

---

## WebSocket (`adapters/websocket`)

Used by examples/iot-device for real-time device command streaming.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WS_READ_BUFFER_SIZE` | `4096` | WebSocket read buffer size in bytes |
| `WS_WRITE_BUFFER_SIZE` | `4096` | WebSocket write buffer size in bytes |
| `WS_HANDSHAKE_TIMEOUT` | `10s` | WebSocket handshake timeout |
| `WS_ALLOWED_ORIGINS` | `*` | Comma-separated list of allowed origins (`*` = all) |
| `WS_PING_INTERVAL` | `30s` | Keepalive ping interval |
| `WS_PONG_TIMEOUT` | `60s` | Pong response timeout before connection close |
| `WS_MAX_MESSAGE_SIZE` | `65536` | Maximum incoming message size in bytes |

### YAML Example

```yaml
websocket:
  readBufferSize: 4096
  writeBufferSize: 4096
  handshakeTimeout: "10s"
  allowedOrigins:
    - "https://app.example.com"
  pingInterval: "30s"
  pongTimeout: "60s"
  maxMessageSize: 65536
```

---

## Validation and Secrets Management

- All `_(required)_` values without defaults will cause the adapter's
  `ConfigFromEnv()` to return an error if the variable is unset or empty.
- Secrets (passwords, keys, tokens) must never be committed to source control.
  Use `.env` files (git-ignored) locally and secrets managers (AWS Secrets
  Manager, HashiCorp Vault) in production.
- The `${VAR}` syntax in YAML examples is expanded by `runtime/config` from
  environment variables at load time.

## Related Documents

- [Integration Testing Guide](./integration-testing.md)
- [Cell Development Guide](./cell-development-guide.md)
- `src/adapters/*/doc.go` — per-adapter godoc with ConfigFromEnv usage
