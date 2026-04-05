# Adapter Configuration Reference

> Configuration options for all GoCell adapters.

## PostgreSQL (`adapters/postgres`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `dsn` | string | required | PostgreSQL connection string |
| `maxOpenConns` | int | 25 | Maximum open connections |
| `maxIdleConns` | int | 5 | Maximum idle connections |
| `connMaxLifetime` | duration | 5m | Maximum connection lifetime |
| `connMaxIdleTime` | duration | 1m | Maximum idle time before close |
| `migrationsDir` | string | `migrations/` | Directory for schema migration files |
| `outboxPollInterval` | duration | 1s | Outbox poller polling interval |
| `outboxBatchSize` | int | 100 | Max entries per outbox poll cycle |

### DSN Format

```
postgres://user:password@host:5432/dbname?sslmode=disable
```

### Environment Variable

`GOCELL_POSTGRES_DSN`

---

## Redis (`adapters/redis`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `addr` | string | `localhost:6379` | Redis server address |
| `password` | string | `""` | Authentication password |
| `db` | int | 0 | Database number |
| `poolSize` | int | 10 | Connection pool size |
| `minIdleConns` | int | 3 | Minimum idle connections |
| `dialTimeout` | duration | 5s | Connection timeout |
| `readTimeout` | duration | 3s | Read timeout |
| `writeTimeout` | duration | 3s | Write timeout |
| `idempotencyTTL` | duration | 24h | TTL for idempotency keys |

### Environment Variable

`GOCELL_REDIS_ADDR`

---

## RabbitMQ (`adapters/rabbitmq`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `url` | string | required | AMQP connection URL |
| `prefetchCount` | int | 10 | Per-consumer prefetch count |
| `reconnectDelay` | duration | 5s | Delay before reconnection attempt |
| `maxRetries` | int | 3 | Max delivery attempts before DLQ |
| `publishConfirm` | bool | true | Enable publisher confirms |
| `dlqSuffix` | string | `.dlq` | Dead-letter queue name suffix |

### URL Format

```
amqp://user:password@host:5672/vhost
```

### Environment Variable

`GOCELL_RABBITMQ_URL`

### Dead-Letter Queue Naming

For a queue named `cg-audit-session-created`, the DLQ is `cg-audit-session-created.dlq`.

---

## OIDC (`adapters/oidc`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `issuerURL` | string | required | OIDC issuer URL (with realm) |
| `clientID` | string | required | OAuth2 client ID |
| `clientSecret` | string | required | OAuth2 client secret |
| `redirectURL` | string | required | Callback URL after authentication |
| `scopes` | []string | `["openid","profile","email"]` | Requested OIDC scopes |
| `clockSkew` | duration | 1m | Allowed clock skew for token expiry |
| `jwksCacheTTL` | duration | 1h | JWKS signing key cache duration |

### Environment Variables

- `GOCELL_OIDC_ISSUER_URL`
- `GOCELL_OIDC_CLIENT_ID`
- `GOCELL_OIDC_CLIENT_SECRET`

---

## S3 (`adapters/s3`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `endpoint` | string | `https://s3.amazonaws.com` | S3 endpoint URL |
| `region` | string | `us-east-1` | AWS region |
| `bucket` | string | required | Default bucket name |
| `accessKeyID` | string | required | AWS access key ID |
| `secretAccessKey` | string | required | AWS secret access key |
| `usePathStyle` | bool | false | Use path-style URLs (required for MinIO) |
| `presignExpiry` | duration | 15m | Presigned URL expiration |
| `uploadPartSize` | int64 | 5MB | Multipart upload part size |

### Environment Variables

- `GOCELL_S3_ENDPOINT`
- `GOCELL_S3_BUCKET`
- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`

---

## WebSocket (`adapters/websocket`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `readBufferSize` | int | 1024 | WebSocket read buffer size (bytes) |
| `writeBufferSize` | int | 1024 | WebSocket write buffer size (bytes) |
| `pingInterval` | duration | 30s | Interval between ping frames |
| `pongTimeout` | duration | 60s | Timeout waiting for pong response |
| `maxMessageSize` | int64 | 524288 | Maximum message size (512KB) |
| `writeTimeout` | duration | 10s | Timeout for write operations |
| `maxConnections` | int | 1000 | Maximum concurrent connections |

---

## Common Patterns

### Loading Configuration

All adapters accept a typed config struct. The recommended pattern:

```go
cfg := postgres.Config{
    DSN:            os.Getenv("GOCELL_POSTGRES_DSN"),
    MaxOpenConns:   25,
    MaxIdleConns:   5,
    ConnMaxLifetime: 5 * time.Minute,
}
adapter, err := postgres.New(cfg)
```

### Graceful Shutdown

Every adapter implements a `Close()` method that must be called during
application shutdown (typically in the assembly Stop phase):

```go
func (a *CoreAssembly) Stop(ctx context.Context) error {
    // Cells are stopped in reverse order
    // Each cell's Stop() calls adapter.Close()
}
```

### Health Checks

Adapters expose health information through the Cell health interface.
Unhealthy adapters cause the owning Cell to report degraded status.
