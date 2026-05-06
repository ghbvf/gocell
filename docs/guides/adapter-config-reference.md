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

### Connection Config (`Config`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `URL` | string | yes | - | AMQP connection URL (`amqp://user:pass@host:5672/`) |
| `ReconnectMaxBackoff` | duration | no | 30s | Maximum backoff duration between reconnect attempts |
| `ReconnectBaseDelay` | duration | no | 1s | Initial delay for exponential backoff |
| `ChannelPoolSize` | int | no | 10 | Maximum number of channels in the pool |
| `ConfirmTimeout` | duration | no | 5s | Timeout for publisher confirm mode |
| `ConnectTimeout` | duration | no | 5s | Timeout for AMQP TCP dial + handshake. NewConnection wires this into `amqp.Config.Dial` via `amqp.DefaultDial(d)`. Aligned with `adapters/postgres` `Config.ConnectTimeout`. |
| `MaxChannelsPerConn` | int | no | 256 | Cap for in-flight channels per Connection. Pool-miss acquisitions return `ErrAdapterAMQPChannelMaxExceeded` when reached. (Closes PR#402 doc drift.) |

#### Migration note: `MaxReconnectAttempts` removed (PR-V1-RMQ-TERMINAL, 029 A4)

The `MaxReconnectAttempts` field was removed. It had been silently ignored
since PR#173 (A.1) made runtime reconnect unbounded. PR-V1-RMQ-TERMINAL
keeps that unbounded retry but adds **soft permanent classification**: when
the broker rejects the handshake (revoked credentials, deleted vhost, hard
protocol error), `Connection.Health()` and `WaitConnected()` return
`ErrAdapterAMQPConnectPermanent` while the reconnect goroutine keeps trying
in the background. `/readyz` flips to 503 (status code only — the
`rabbitmq_ready` probe text is exposed only on `/readyz?verbose=true`,
see `docs/ops/readyz.md`); a successful dial after operator remediation
clears the classification automatically (no pod restart required).

There is no replacement field — the design intentionally has no per-config
attempt cap. Behavior is now uniform across all `Config` instances:
`ReconnectMaxBackoff` caps the backoff delay; the reconnect loop runs until
`Close` is called.

#### Migration note: `ConnectTimeout` default 5s (PR-V1-RMQ-CONFORMANCE-AND-CLOSURE, 029 B13)

`Config.ConnectTimeout` was added with a default of `5 * time.Second`,
wired into `amqp.Config.Dial` via `amqp.DefaultDial(d)`. Before this PR,
`NewConnection` called `amqp.Dial(url)` bare, which inherited the OS
default TCP SYN timeout (~1 minute on Linux, ~75 seconds on macOS) — an
unreachable broker could block `NewConnection` for over a minute.

Behavior change after upgrade (no code changes required for default users):

- A broker that is reachable but slow to handshake (TLS, network jitter,
  loaded broker) now fails dial after 5s instead of relying on the OS
  default. The `Connection` reconnect loop immediately backs off and
  retries (`ReconnectBaseDelay` 1s → `ReconnectMaxBackoff` 30s), so a
  blip self-heals on the next successful dial.
- Typical symptom of the new default biting too aggressively: `slog.Warn
  "rabbitmq: reconnect attempt"` repeating with `error` field showing
  `i/o timeout` on every attempt against a broker that does eventually
  accept connections under a longer budget.

Tuning advice:

- Default 5s suits AMQP over a healthy LAN/cloud-internal network and
  matches `adapters/postgres` `Config.ConnectTimeout` parity.
- Slow / cross-region links or heavy mTLS handshakes: raise to **10–15s**
  via `Config.ConnectTimeout`.
- Tests against a blackhole IP / fault-injection harness: use a tight
  value like `200ms` (see `adapters/rabbitmq/connect_timeout_test.go`
  `TestNewConnection_ConnectTimeout_Blackhole`).
- The `connect_timeout` `slog.Duration` field is logged on every
  successful `connect()` (`adapters/rabbitmq/connection.go`); use it to
  audit the effective value in production after deploy.

### ConsumerBase Config (`ConsumerBaseConfig`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `ConsumerGroup` | string | yes | - | Consumer group identifier for idempotency keys |
| `RetryCount` | int | no | 3 | Maximum retries for transient errors |
| `RetryBaseDelay` | duration | no | 1s | Initial delay for exponential backoff retries |
| `IdempotencyTTL` | duration | no | 24h | TTL for idempotency done-keys |
| `LeaseTTL` | duration | no | 5m | Processing-lease TTL for Claimer backend |
| `ClaimPolicy` | ClaimPolicy | no | ClaimPolicyFailClosed (zero-value) | `ClaimPolicyFailOpen`: proceed without idempotency on Claim failure; `ClaimPolicyFailClosed`: requeue until backend recovers |
| `ClaimRetryCount` | int | no | RetryCount | Max Claim() attempts on the fail-closed path |
| `ClaimRetryBaseDelay` | duration | no | RetryBaseDelay | Initial backoff between Claim() retries |
| `MaxRetryDelay` | duration | no | 30s | Cap for exponential backoff delay |

### Subscriber Config (`SubscriberConfig`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `DLXExchange` | string | yes | - | Dead-letter exchange name. Without it, broker `Nack(requeue=false)` silently discards messages. Set per-cell DLX. |
| `Clock` | clock.Clock | yes | - | Time source for `StopIntake` deadlines (`StopIntakePerCallTimeout`, `StopIntakeDrainTimeout`). `NewSubscriber` calls `clock.MustHaveClock` and panics if nil; pass `clock.Real()` at composition root or `clockmock.FakeClock` in tests. |
| `QueueName` | string | no | - | Explicit queue name. Takes precedence over ConsumerGroup-based naming. |
| `ConsumerGroup` | string | no | - | Logical consumer group. When QueueName is empty, queue is derived as `{ConsumerGroup}.{topic}`. |
| `DLXRoutingKey` | string | no | "" | Routing key for dead-lettered messages (only effective when DLXExchange is set). |
| `PrefetchCount` | int | no | 10 | Prefetch (QoS) count per consumer. |
| `StopIntakePerCallTimeout` | duration | no | 2s | Bound for any single `basic.cancel` during StopIntake. A hung broker cannot stall shutdown beyond this budget per consumer. |
| `StopIntakeDrainTimeout` | duration | no | 30s | Total upper bound for StopIntake to drain in-flight prefetched deliveries + handler completion. Exceeding it returns `ErrAdapterAMQPCloseTimeout` and logs `slog.Warn` with remaining inflight count. |

## OIDC (`adapters/oidc`) — thin go-oidc v3 wrapper

Exposes `coreos/go-oidc` and `golang.org/x/oauth2` types directly. No GoCell wrapper types.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `issuerURL` | string | yes | - | OIDC provider issuer URL |
| `clientID` | string | yes | - | OAuth2 client ID |
| `clientSecret` | string | no | - | OAuth2 client secret |
| `redirectURL` | string | no | - | OAuth2 callback URL |
| `scopes` | []string | no | ["openid","profile","email"] | Requested scopes |
| `httpTimeout` | duration | no | 10s | HTTP client timeout for discovery/token calls |

Provides: `Provider()`, `Refresh()`, `Verifier()`, `OAuth2Config()`. For token exchange and userinfo, use the returned `oauth2.Config` and `go-oidc` provider directly.

## S3 (`adapters/s3`) — thin aws-sdk-go-v2 wrapper

Implements `ObjectUploader` interface (Upload only). For download, delete, presigned URLs, use `client.SDK()` to access the underlying `*s3.Client`.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `endpoint` | string | yes | - | S3-compatible endpoint URL |
| `region` | string | yes | - | AWS region |
| `bucket` | string | yes | - | Default bucket name |
| `accessKeyID` | string | yes | - | Access key ID |
| `secretAccessKey` | string | yes | - | Secret access key |
| `usePathStyle` | bool | no | false | Use path-style addressing (required for MinIO) |
| `httpTimeout` | duration | no | 30s | HTTP client timeout |

Provides: `Upload()`, `Health()`, `SDK()`.

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
