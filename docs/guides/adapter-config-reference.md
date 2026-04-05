# Adapter Configuration Reference

This document lists every adapter shipped with GoCell and its configuration surface.

## PostgreSQL (`adapters/postgres`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `dsn` | string | yes | - | PostgreSQL connection string (`postgres://user:pass@host:5432/db`) |
| `maxConns` | int | no | 10 | Maximum open connections in the pool |
| `minConns` | int | no | 2 | Minimum idle connections kept alive |
| `maxConnLifetime` | duration | no | 30m | Maximum lifetime of a connection |
| `maxConnIdleTime` | duration | no | 5m | Maximum idle time before a connection is closed |
| `migrationDir` | embed.FS | no | - | Embedded filesystem containing SQL migration files |

### Outbox Relay

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `pollInterval` | duration | no | 1s | How often the relay polls for unsent outbox rows |
| `batchSize` | int | no | 100 | Maximum rows fetched per poll cycle |
| `publisher` | outbox.Publisher | yes | - | Target publisher (typically RabbitMQ) |

## Redis (`adapters/redis`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `addr` | string | yes | - | Redis server address (`host:port`) |
| `password` | string | no | "" | AUTH password |
| `db` | int | no | 0 | Database number |
| `maxRetries` | int | no | 3 | Maximum command retries on transient errors |
| `dialTimeout` | duration | no | 5s | Connection dial timeout |
| `readTimeout` | duration | no | 3s | Read timeout per command |
| `writeTimeout` | duration | no | 3s | Write timeout per command |
| `poolSize` | int | no | 10 | Connection pool size |

### Distributed Lock

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `lockTTL` | duration | no | 30s | Lock expiry TTL |
| `retryDelay` | duration | no | 100ms | Delay between lock acquisition retries |
| `retryCount` | int | no | 3 | Maximum lock acquisition retries |

### Idempotency Store

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `keyPrefix` | string | no | "idem" | Redis key prefix for idempotency keys |
| `ttl` | duration | no | 24h | Idempotency key expiry |

## RabbitMQ (`adapters/rabbitmq`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | yes | - | AMQP connection URL (`amqp://user:pass@host:5672/`) |
| `reconnectDelay` | duration | no | 5s | Delay before reconnection attempt |
| `maxReconnect` | int | no | 10 | Maximum reconnection attempts (0 = unlimited) |

### Publisher

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `exchange` | string | yes | - | Target exchange name |
| `exchangeType` | string | no | "topic" | Exchange type (topic, direct, fanout) |
| `confirmMode` | bool | no | true | Enable publisher confirms |

### ConsumerBase

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `queue` | string | yes | - | Queue to consume from |
| `consumerTag` | string | no | auto | Consumer tag for identification |
| `prefetchCount` | int | no | 10 | Prefetch (QoS) count |
| `retryMax` | int | no | 3 | Maximum retries before dead-lettering |
| `retryBackoff` | duration | no | 1s | Initial backoff between retries |
| `dlqExchange` | string | no | "" | Dead-letter exchange (empty = default DLX) |
| `idempotencyStore` | idempotency.Store | no | nil | Idempotency checker (e.g., Redis store) |

## OIDC (`adapters/oidc`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `issuerURL` | string | yes | - | OIDC provider issuer URL |
| `clientID` | string | yes | - | OAuth2 client ID |
| `clientSecret` | string | yes | - | OAuth2 client secret |
| `redirectURL` | string | yes | - | OAuth2 callback URL |
| `scopes` | []string | no | ["openid","profile","email"] | Requested scopes |
| `discoveryTimeout` | duration | no | 10s | Timeout for discovery document fetch |
| `jwksRefreshInterval` | duration | no | 1h | JWKS key set refresh interval |

## S3 (`adapters/s3`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `endpoint` | string | yes | - | S3-compatible endpoint URL |
| `region` | string | no | "us-east-1" | AWS region |
| `bucket` | string | yes | - | Default bucket name |
| `accessKey` | string | yes | - | Access key ID |
| `secretKey` | string | yes | - | Secret access key |
| `usePathStyle` | bool | no | true | Use path-style addressing (required for MinIO) |
| `presignExpiry` | duration | no | 15m | Default presigned URL expiry |

## WebSocket (`adapters/websocket`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `readLimit` | int64 | no | 65536 | Maximum message size in bytes |
| `writeTimeout` | duration | no | 10s | Write deadline per message |
| `pingInterval` | duration | no | 30s | Interval between server-side pings |
| `maxConnsPerUser` | int | no | 5 | Maximum concurrent connections per user |
| `authFunc` | func | yes | - | Function to authenticate the upgrade request |

## Usage Pattern

All adapters follow the Option function pattern:

```go
pool, err := postgres.NewPool(
    postgres.WithDSN("postgres://..."),
    postgres.WithMaxConns(20),
)
```

See `docs/guides/cell-development-guide.md` for how to inject adapters into Cells.
