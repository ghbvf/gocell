# WebSocket Integration Guide

> Applicable version: GoCell v0.x

---

## 1. Architecture Overview

```
HTTP request
    │
    ▼
adapters/websocket.UpgradeHandler   — transport layer
    ├─ Authenticate (before Accept)
    ├─ websocket.Accept (coder/websocket)
    └─ hub.Register(conn)
            │
            ▼
    runtime/websocket.Hub            — application layer
        ├─ connMu + conns map
        ├─ subjectIdx (O(1) subject → conns)
        ├─ pingLoop (goroutine)
        └─ per-conn readLoop + writeLoop (goroutines)
```

**Responsibility boundaries**:

| Layer | Package | Responsibility |
|---|---|---|
| Transport | `adapters/websocket` | HTTP upgrade, Origin validation, authentication, conn encapsulation |
| Application | `runtime/websocket` | Connection lifecycle, heartbeat eviction, broadcast routing |

`adapters/websocket` relies on `coder/websocket` for frame-level protocol handling; `runtime/websocket.Hub` knows nothing about transport details and interacts only with the `Conn` interface.

---

## 2. Origin Configuration

`UpgradeConfig.AllowedOrigins` is a security-critical field:

```go
cfg := adapterws.UpgradeConfig{
    AllowedOrigins: []string{
        "https://app.example.com",
        "https://*.example.com",   // wildcard covering one host segment only
    },
    Authenticator: auth.NewContextAuthenticator(),
}
handler, err := adapterws.UpgradeHandler(hub, cfg)
```

Rules:

- **Required and non-empty**: empty slice → `errcode.ErrWebsocketOriginsMissing`, construction fails.
- **Scheme required**: `"example.com"` without scheme is rejected (`errcode.ErrWebsocketOriginsInvalid`); browser Origin headers always include the scheme, so a bare host will never match.
- **Wildcard `"*"` is forbidden**: explicitly rejected, routes to `errcode.ErrWebsocketOriginsInvalid`.
- **Wildcard covers one host segment only**: `"https://*.example.com"` is valid; `"https://**"` has undefined semantics and must be avoided.

> **Production warning**: `"http://*"` and `"https://*"` full-host wildcards are only for local development or intranet debugging; production environments must use specific host patterns such as `"https://app.example.com"` or `"https://*.app.example.com"`. Full-host wildcards bypass the Origin security boundary, allowing arbitrary cross-origin access.

---

## 3. Authentication Integration

`UpgradeConfig.Authenticator` is required (nil → `errcode.ErrWebsocketAuthenticatorMissing`). Authentication runs before `websocket.Accept`; authentication failure writes a `401 Unauthorized` plain response directly (browser WebSocket API cannot read a response body, so a JSON envelope is meaningless).

### 3.1 Three Integration Modes

#### Bearer token via Authorization header

```go
// Suitable for: server-to-server connections (curl, native app, backend worker)
// Limitation: browser JS WebSocket API cannot set the Authorization header
// verifier type is auth.IntentTokenVerifier, implementing VerifyIntent(ctx, token, expected TokenIntent) (Claims, error)
authenticator := auth.NewBearerHeaderAuthenticator(verifier)
```

> When the WebSocket route is mounted behind an existing JWT listener, prefer `auth.NewContextAuthenticator()` (next section) to avoid double verification. The Bearer header authenticator is only for scenarios where the WebSocket runs on a dedicated port with no JWT validation at the listener level.

#### Pass-through after listener middleware authentication (recommended for `/api/v1/*`)

```go
// Suitable for: WebSocket routes mounted on an existing JWT listener
// Principal is written to ctx by the listener JWT middleware; Authenticator reads it out
authenticator := auth.NewContextAuthenticator()
```

Prefer this when the WebSocket handler is registered on PrimaryListener: the listener has already validated the JWT, avoiding double verification.

#### Explicit anonymous (broadcast-only channels)

```go
// Suitable for: hubs that push only public data (announcement channels, market data)
// Must be declared explicitly; nil is not a valid substitute
authenticator := auth.NewAnonymousAuthenticator()
```

### 3.2 Custom AuthenticatorFunc

The browser JS `WebSocket` API does not support setting the `Authorization` header. Common alternatives:

#### a. Query-param token

```go
authenticator := auth.AuthenticatorFunc(func(r *http.Request) (*auth.Principal, bool, error) {
    token := r.URL.Query().Get("token")
    if token == "" {
        return nil, false, nil
    }
    _, principal, err := auth.AuthenticateBearer(r.Context(), verifier, token)
    if err != nil {
        return nil, false, err
    }
    return principal, true, nil
})
```

**Security trade-off**: the token appears in the URL and will end up in server access logs, browser history, and proxy logs. Use this only when cookies are not an option, and set a very short TTL (≤ 60 s, one-time token).

#### b. Cookie (recommended for browser scenarios)

```go
authenticator := auth.AuthenticatorFunc(func(r *http.Request) (*auth.Principal, bool, error) {
    cookie, err := r.Cookie("session_token")
    if err != nil {
        return nil, false, nil
    }
    _, principal, err := auth.AuthenticateBearer(r.Context(), verifier, cookie.Value)
    if err != nil {
        return nil, false, err
    }
    return principal, true, nil
})
```

**Security trade-off**: the cookie does not appear in the URL; set `SameSite=Strict` (or `Lax`) + `HttpOnly` + `Secure` to prevent CSRF and XSS.

#### c. Sec-WebSocket-Protocol sub-protocol carrying the token

```go
authenticator := auth.AuthenticatorFunc(func(r *http.Request) (*auth.Principal, bool, error) {
    // Browser can pass sub-protocols via new WebSocket(url, ["v1", "<token>"])
    protos := r.Header.Get("Sec-WebSocket-Protocol")
    // Parse out the token portion...
    ...
})
```

**Security trade-off**: the token appears in plaintext in the handshake header but not in URL logs; the server must echo back the selected sub-protocol in the Accept response, making the implementation slightly more complex.

### 3.3 Reading Principal in a MessageHandler

Hub injects the Principal into the per-connection context at Register time (`auth.WithPrincipal(connCtx, p)`). The `ctx` received by a MessageHandler can directly obtain the principal via `auth.FromContext(ctx)`, without going through the Conn object:

```go
hub := rtws.NewHub(cfg, func(ctx context.Context, connID string, data []byte) {
    p, ok := auth.FromContext(ctx)
    if !ok || p == nil {
        slog.Warn("ws: message from unauthenticated conn", slog.String("conn_id", connID))
        return
    }
    // p.Subject / p.Roles / p.ExpiresAt are read-only snapshots taken at handshake time; do not modify them.
    _ = p.Subject
    _ = p.Roles
    // Business logic...
})
```

### 3.4 Principal Immutability Convention

After the Authenticator returns `*auth.Principal`, callers **must not modify** any of its fields (`Subject` / `Roles` / `ExpiresAt` / `Claims`). Hub snapshots `subject` and `expiresAt` from `conn.Principal()` into `connEntry` at handshake time; after registration the hub does not re-read `conn.Principal()`. The `Conn.Principal()` field must remain unchanged for the entire connection lifetime; to refresh an identity, the client must re-handshake.

### 3.5 Composition Root Examples

```go
// Option 1: ContextAuthenticator (recommended for /api/v1/* JWT listener)
handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
    AllowedOrigins: []string{"https://app.example.com"},
    Authenticator:  auth.NewContextAuthenticator(),
})

// Option 2: Bearer header authenticator (dedicated port, self-validating Bearer header)
// verifier implements the auth.IntentTokenVerifier interface
handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
    AllowedOrigins: []string{"https://app.example.com"},
    Authenticator:  auth.NewBearerHeaderAuthenticator(verifier),
})

// Option 3: AnonymousAuthenticator (broadcast channel, no authentication)
handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
    AllowedOrigins: []string{"https://app.example.com"},
    Authenticator:  auth.NewAnonymousAuthenticator(),
})
```

### 3.6 Service Principal

A service token's identity is expressed through `CallerCellID`, **not** `Subject` (service principal `Subject` is always empty). Filter service connections by reading `p.CallerCellID`:

```go
// Filter for service connections of a specific cell by CallerCellID
err := hub.BroadcastFilter(ctx, data, func(c rtws.Conn) bool {
    p := c.Principal()
    return p != nil && p.CallerCellID == "accesscore"
})
```

---

## 4. Heartbeat and Token Renewal

Hub has a built-in ping-pong loop:

- **PingInterval** (default 30 s): sends a ping to all connections each round.
- **PingMissMax** (default 2): evicts the connection when consecutive misses reach the threshold.
- **PingTimeout** (default 5 s): deadline for a single ping.

**Token expiry eviction**: before sending pings each round, the ping loop checks `Principal.ExpiresAt`. If the current time is past `ExpiresAt`, the connection is evicted without waiting for the next miss. `ExpiresAt.IsZero()` means no check (Anonymous principals do not expire). Token expiry evictions include a `reason="token_expired"` structured field in slog.

**Server-side push refresh is not supported in GoCell v0.x**: token renewal must be initiated by the client:

1. Client detects that the token is about to expire (recommend at least 60 s before expiry).
2. Client obtains a new token from the original authentication API.
3. Client actively closes the WS connection and re-handshakes with the new token.

**In the worst case**, an expired token will be evicted within at most `PingInterval` (default 30 s); reduce `PingInterval` for sensitive scenarios.

---

## 5. Reconnection Strategy

Clients should implement exponential backoff reconnection (recommended parameters):

```
Initial delay: 1 s
Multiplier:    2
Max delay:     30 s
Jitter:        ±500 ms (to prevent thundering herd)
```

Example sequence: `1 s → 2 s → 4 s → 8 s → 16 s → 30 s → 30 s → ...`

### HTTP Status Code Reference

| Status code | Trigger | Client action |
|---|---|---|
| 101 | Upgrade successful | — |
| 400 | Client handshake protocol violation (missing `Sec-WebSocket-Key`, Origin rejected, non-GET, etc.) | Fix client implementation; do not retry |
| 401 | Missing or invalid credentials | Re-obtain token / re-authenticate |
| 500 | Server error (hijack not supported, Accept I/O failure) | Retry with exponential backoff |
| 503 | Hub not started or shutting down | Retry with exponential backoff; wait for readyz to pass |

Server-side behaviour:

- When `hub.IsRunning() == false`, `UpgradeHandler` returns `503 Service Unavailable`. Clients receiving 503 should continue retry with backoff.
- When the hub stops (`Stop` called or `Start` ctx cancelled), all connections are closed. Client reconnection requests will continue to receive 503 until the hub is ready again.
- Normal shutdown sequence: stop the hub first (closing connections); readyz returns 503 to prevent the LB from routing new requests.

---

## 6. Broadcasting: BroadcastFilter vs BroadcastToSubject

### BroadcastFilter — General Filtered Broadcast

```go
// filter is required; nil returns errcode.ErrWebsocketBroadcastFilterMissing
err := hub.BroadcastFilter(ctx, data, func(c rtws.Conn) bool {
    p := c.Principal()
    return p != nil && p.CallerCellID == "accesscore"
})

// Full broadcast (explicit)
err := hub.BroadcastFilter(ctx, data, func(rtws.Conn) bool { return true })
```

Characteristics:

- O(N) iteration over all connections.
- **filter runs outside the lock**: Hub snapshots the connection list under `connMu` then releases the lock before invoking the filter for each connection. A slow filter therefore does not block `Register` / `Stop`, and it is **safe** to call `hub.Send(...)` / `hub.BroadcastToSubject(...)` inside a filter without risking a deadlock.
- **filter should still be O(1) cheap**: a slow filter only increases the latency of that broadcast (iteration starts after the snapshot), without affecting connection management. Database queries or remote RPCs inside a filter are forbidden (N connections × RPC latency = broadcast latency amplification anti-pattern).

### BroadcastToSubject — Subject-indexed Broadcast

```go
// O(1) index lookup, locates connections directly via subjectIdx
// subject == "" returns errcode.ErrWebsocketBroadcastSubjectMissing
// subject not found (no connections) → noop, returns nil
err := hub.BroadcastToSubject(ctx, userID, data)
```

Characteristics:

- `subjectIdx` is maintained by Hub at Register/Unregister time, strictly in sync with `conns`.
- Subject comes from `conn.Principal().Subject` (JWT sub claim). Service principal Subject is empty and does not enter `subjectIdx`; service connections should be routed via `BroadcastFilter` + `CallerCellID`.
- Anonymous principals (Subject == "") do not enter `subjectIdx`.

### ctx Behaviour

`Send` / `BroadcastFilter` / `BroadcastToSubject` check the caller's ctx before enqueuing:

- If the caller's ctx is already cancelled, they return `ctx.Err()` immediately without delivering any message to the send channel (short-circuit).
- **ctx only controls enqueue timeout**; writeLoop writes to the socket using an internal per-connection `connCtx`; messages successfully enqueued before the caller's ctx is cancelled **will still** be delivered by the writeLoop.
- The caller's ctx does not pollute the send channel (a cancelled ctx will never write a message to the channel).

### Multi-Tenant ACL Example

```go
// Targeted push by subject (user data change notification)
func notifyUser(hub *rtws.Hub, userID string, event []byte) error {
    return hub.BroadcastToSubject(ctx, userID, event)
}

// Filtered broadcast by cell (push only to service connections of a specific cell)
func broadcastToCell(hub *rtws.Hub, cellID string, event []byte) error {
    return hub.BroadcastFilter(ctx, event, func(c rtws.Conn) bool {
        p := c.Principal()
        return p != nil && p.CallerCellID == cellID
    })
}
```

---

## 7. Slow Client Eviction

Each connection has its own send channel; capacity is controlled by `HubConfig.SendBufferSize` (default 32). **A zero value automatically falls back to the default of 32** (same pattern as `PingInterval`, `PingMissMax`, etc.); there is no "0 = unbuffered" semantics.

**Eviction triggers**:

- During `BroadcastFilter` / `BroadcastToSubject` fanout, if a connection's `send` channel is full → immediate eviction (select default-drop).
- During `Send(connID)`, if the channel is full → eviction + returns `errcode.ErrWebsocketSlowClient`.
- Inside `writeLoop`, if `conn.Write` fails (network error) → eviction via the same path.

Clients must tolerate the server actively closing the connection. Upon receiving `1001 Going Away` or EOF, apply the reconnection backoff logic.

**evict slog `reason` field**: all eviction paths include a structured `reason` field in slog output, allowing operators to distinguish eviction causes by label:

| `reason` value | Trigger path |
|---|---|
| `send_buffer_full` | Slow client: send channel full, triggered by fanout or Send |
| `connection_write_failed` | writeLoop socket write failure (network interruption) |
| `token_expired` | Ping loop detects `Principal.ExpiresAt` is past |
| `duplicate_conn_id` | Register finds an existing connection with the same ID; old connection is evicted |

---

## 8. Fault Injection and Load Testing

### fakeConn Mode

`runtime/websocket/hub_test.go` provides a `fakeConn` reference implementation:

```go
// Normal connection (principal is snapshotted into connEntry.subject/expiresAt at handshake time)
conn := newFakeConnWithPrincipal("conn-1", &auth.Principal{Kind: auth.PrincipalUser, Subject: "user-1"})
require.NoError(t, hub.Register(ctx, conn))

// Blocking connection (simulates slow client; writeLoop blocks forever → evicted when send buffer fills)
slow := newBlockingFakeConn("slow-1", &auth.Principal{Kind: auth.PrincipalUser, Subject: "slow"})
require.NoError(t, hub.Register(ctx, slow))
// After triggering BroadcastFilter, the conn's send buffer fills → eviction (reason="send_buffer_full")
```

### Advancing Token Expiry with clockmock

`clockmock.New(initial time.Time)` returns `*FakeClock`; the parameter is the initial instant (`time.Time`), **not** `*testing.T`:

```go
clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
hub := rtws.NewHub(rtws.DefaultHubConfig(clk), nil)

p := &auth.Principal{
    Kind:      auth.PrincipalUser,
    Subject:   "user-1",
    ExpiresAt: clk.Now().Add(5 * time.Minute),
}
conn := newFakeConnWithPrincipal("conn-1", p)
require.NoError(t, hub.Register(ctx, conn))

// Advance the clock past the token expiry
clk.Advance(6 * time.Minute)

// The next ping tick triggers expiry eviction
clk.Advance(hub.Config().PingInterval)

require.Eventually(t, func() bool {
    return hub.ConnCount() == 0
}, time.Second, 10*time.Millisecond)
```

### Async Assertions for BroadcastToSubject

`BroadcastToSubject` delivers data to the writeLoop goroutine's channel; assertions must wait for asynchronous completion:

```go
err := hub.BroadcastToSubject(ctx, "user-1", []byte("hello"))
require.NoError(t, err)

// Wait for writeLoop to actually deliver
require.Eventually(t, func() bool {
    return conn.ReceivedCount() == 1
}, time.Second, time.Millisecond)
```

---

## 9. Operational Parameters

| Field | DefaultHubConfig value | Tuning trigger |
|---|---|---|
| `PingInterval` | `30 s` | Decrease (e.g. 10 s) when network is unstable; increase (e.g. 60 s) to reduce ping overhead with high connection counts |
| `PingTimeout` | `5 s` | Increase for high-latency networks (e.g. cross-continent); decrease for faster dead-connection detection |
| `ReadLimit` | `64 KB` | Increase when message payloads exceed the limit; decrease if a lower security boundary is needed |
| `PingMissMax` | `2` | Increase (3–5) for higher jitter tolerance; set to 1 for strict liveness detection |
| `MaxConnections` | `0` (unlimited) | Set a limit (e.g. 10000) to prevent OOM; match to CPU/memory capacity |
| `SendBufferSize` | `32`; **zero value automatically uses default 32** | Increase (64–256) for high-throughput push; decrease for strict fail-closed behaviour |
| `Clock` | No default; must be provided | `clock.Real()` for production; `clockmock.New(time.Now())` for tests |

**evict slog `reason` field**: all connection evictions attach a `reason` field in slog, usable for alert classification: `send_buffer_full` (slow client), `token_expired` (token expired), `connection_write_failed` (write failure / network interruption), `duplicate_conn_id` (duplicate connection ID).

---

## Further Reading

- Architecture decision: `docs/architecture/202605011500-adr-ws-auth-acl.md` (SEC-FAIL-CLOSED design)
- Error code reference: `pkg/errcode/errcode.go` (`ErrWebsocket*` series)
- Archtest rules: `tools/archtest/security_defaults_test.go` (SEC-07/08/09)

ref: coder/websocket accept.go; centrifugal/centrifuge hub.go; olahol/melody hub.go
