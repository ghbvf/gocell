# R1D-6: adapters/websocket Six-Role Review

| Field | Value |
|---|---|
| Reviewer Seats | S1 Architecture + S2 Security + S3 Test + S4 Ops + S5 DX + S6 Product |
| Scope | `src/adapters/websocket/` (4 prod files, 2 test files, ~386 LOC prod / ~292 LOC test) |
| Review basis commit | `5096d4f` (HEAD) |
| Date | 2026-04-06 |

---

## Executive Summary

`adapters/websocket` is small and layer-clean, but the six-role review found **4 P1 findings** and **3 P2 findings**.

The dominant risk is **Hub lifecycle correctness**: with live connections, `Stop()` can block indefinitely because read loops are detached from the Hub lifecycle and the shutdown path waits for them before it closes the underlying sockets. The HTTP boundary is also too permissive by default (`AllowedOrigins == nil` disables same-origin checks), and the read/ping path is too tightly coupled: the message handler runs inline on the sole reader goroutine, so panic or slow business logic can destabilize connection liveness.

| Severity | Count | Summary |
|---|---|---|
| P0 | 0 | -- |
| P1 | 4 | Shutdown deadlock, insecure default origin policy, reader/handler coupling, broadcast HOL blocking |
| P2 | 3 | Hardcoded/unvalidated timing config, docs/spec drift, shallow lifecycle/security tests |

---

## Inventory

| File | LOC | Role in module |
|---|---:|---|
| `doc.go` | 13 | Package contract |
| `errors.go` | 17 | Error codes |
| `handler.go` | 53 | HTTP upgrade boundary |
| `hub.go` | 303 | Connection lifecycle, broadcast, ping loop |
| `hub_test.go` | 261 | Unit tests |
| `integration_test.go` | 31 | Integration placeholders |

---

## Cross-Seat Consensus

1. **Graceful shutdown is broken with active connections** — confirmed independently by S1/S3/S4/S6.
2. **Zero-value upgrade config is insecure** — S2 flagged the same-origin bypass as a boundary-level issue.
3. **The reader goroutine is overloaded with business work** — S1/S4 found that panic or slow handlers can break liveness.
4. **Signal-first broadcast is vulnerable to slow-consumer amplification** — S4/S6 both found head-of-line blocking.
5. **The current tests would not catch the main failures above** — S3 confirmed the risky paths are effectively untested.

---

## F-01: `Stop()` can block forever when live connections exist

| Field | Value |
|---|---|
| Seat | S1 Architecture + S4 Ops + S6 Product |
| Severity | **P1** |
| Category | Lifecycle / Concurrency / Availability |
| Files | `src/adapters/websocket/hub.go:132-155`, `src/adapters/websocket/hub.go:176-181`, `src/adapters/websocket/hub.go:250-258`, `src/adapters/websocket/handler.go:44-47` |
| Status | OPEN |

**Evidence**

- `UpgradeHandler` registers every accepted connection with `context.Background()` instead of a Hub-scoped cancellable context.
- `Register()` adds each `readLoop()` goroutine into `h.wg`.
- `Stop()` ignores its input context, calls `h.wg.Wait()` first, and only closes active connections after the wait returns.
- `readLoop()` exits only when `Read()` returns an error or its context is cancelled.

This creates a wait cycle:

1. Client remains connected.
2. `readLoop()` blocks in `conn.Read(ctx)`.
3. `Stop()` waits for `readLoop()` to exit.
4. `Stop()` never reaches the code that closes the connection and would have unblocked `Read()`.

**Impact**

Any deployment with active WebSocket clients can hang indefinitely during graceful shutdown, rolling restart, or failover. This directly violates the module's "graceful shutdown with connection draining" contract and is especially risky for upper-layer device/order push scenarios where long-lived clients are the norm.

**Recommendation**

- Bind per-connection read loops to a Hub-owned cancellable lifecycle context.
- Mark the Hub as stopping before shutdown starts and reject new registrations during that window.
- In `Stop(ctx)`, actively close/remove connections first, then wait for goroutines to exit, and honor the caller's timeout/cancellation.

---

## F-02: Empty `AllowedOrigins` disables same-origin protection

| Field | Value |
|---|---|
| Seat | S2 Security |
| Severity | **P1** |
| Category | Security / Boundary Validation |
| File | `src/adapters/websocket/handler.go:26-30` |
| Status | OPEN |

**Evidence**

When `AllowedOrigins` is empty, `UpgradeHandler` sets `websocket.AcceptOptions.InsecureSkipVerify = true`. With `nhooyr.io/websocket`, that disables the default same-origin validation instead of falling back to a safe default.

**Impact**

The zero-value config accepts cross-site WebSocket upgrades from arbitrary origins. If an application later relies on browser cookies or any automatically attached credentials, this becomes a Cross-Site WebSocket Hijacking footgun at the adapter boundary.

**Recommendation**

- Treat empty `AllowedOrigins` as "use the library default origin checks", not "disable origin checks".
- If true dev-mode bypass is needed, expose it as an explicit insecure option instead of the zero-value path.

---

## F-03: `MessageHandler` runs inline on the sole reader goroutine

| Field | Value |
|---|---|
| Seat | S1 Architecture + S4 Ops |
| Severity | **P1** |
| Category | Concurrency / Resilience |
| Files | `src/adapters/websocket/hub.go:250-264`, `src/adapters/websocket/hub.go:283-299` |
| Status | OPEN |

**Evidence**

- `readLoop()` invokes the injected `MessageHandler` inline on the same goroutine that performs `conn.Read()`.
- There is no `recover` boundary around the callback.
- The Hub's health-check path uses `Conn.Ping()` and treats a timeout as grounds for forced unregister.
- The underlying `nhooyr.io/websocket` library requires `Ping()` to run concurrently with a reader because pong processing depends on the reader continuing to consume frames.

**Impact**

This coupling creates two production hazards:

1. a panic inside any user-provided `MessageHandler` escapes the goroutine and can crash the whole process;
2. a slow handler blocks the only reader, so pong frames are not consumed in time and healthy connections can be misclassified as dead and dropped.

**Recommendation**

- Decouple socket reads from business handling via a queue/worker model or another handoff boundary.
- Add `recover` around the callback invocation and close only the offending connection on failure.
- Keep a dedicated reader alive while the ping/pong health-check is enabled.

---

## F-04: Broadcast fan-out is head-of-line blocked by slow or stale clients

| Field | Value |
|---|---|
| Seat | S4 Ops + S6 Product |
| Severity | **P1** |
| Category | Performance / Availability |
| Files | `src/adapters/websocket/hub.go:18-19`, `src/adapters/websocket/hub.go:42-47`, `src/adapters/websocket/hub.go:210-225`, `src/adapters/websocket/hub.go:283-299` |
| Status | OPEN |

**Evidence**

- `Broadcast()` iterates all connections serially.
- Each `Conn.Write()` wraps the send in a fixed `10s` timeout.
- Write failures are only logged; the bad connection is not removed until a later ping cycle happens to catch it.

**Impact**

A single slow or half-dead client can delay the entire fan-out path by up to 10 seconds per bad connection. In a "signal-first" adapter, this turns one unhealthy consumer into visible latency for every healthy consumer behind it.

**Recommendation**

- Move to per-connection send queues or bounded concurrent fan-out.
- Immediately unregister connections on deterministic write failures/timeouts instead of waiting for the next ping sweep.
- Make the write timeout configurable if serial writes remain part of the design.

---

## F-05: Timing config is partly hardcoded and invalid values are not validated

| Field | Value |
|---|---|
| Seat | S1 Architecture + S4 Ops |
| Severity | **P2** |
| Category | Configuration / Robustness |
| Files | `src/adapters/websocket/hub.go:18-21`, `src/adapters/websocket/hub.go:63-69`, `src/adapters/websocket/hub.go:98-103`, `src/adapters/websocket/hub.go:270`, `src/adapters/websocket/hub.go:293` |
| Status | OPEN |

**Evidence**

- `NewHub()` only normalizes zero values; it does not reject invalid negative values.
- `pingLoop()` passes `h.config.PingInterval` directly into `time.NewTicker(...)`, which panics on negative durations.
- The write timeout is hardcoded to `10s`.
- The ping timeout is hardcoded to `5s`.

**Impact**

A bad `PingInterval` value can take the process down during startup, while real deployments cannot tune write/pong behavior to match higher-latency networks. The adapter therefore has both a validation gap and a tuning gap in its most important timing knobs.

**Recommendation**

- Validate `PingInterval > 0` and reject invalid config early.
- Expose `WriteTimeout` and pong timeout through `HubConfig` if ping-based health checks remain part of the contract.
- Log the effective timing config at startup so runtime behavior is inspectable.

---

## F-06: Public docs/specs promise auth, user identity, and timeout controls that do not exist

| Field | Value |
|---|---|
| Seat | S5 DX + S6 Product |
| Severity | **P2** |
| Category | API Contract / Documentation Drift |
| Files | `docs/guides/adapter-config-reference.md:108-112`, `specs/feat/002-phase3-adapters/spec.md:88-91`, `specs/feat/002-phase3-adapters/product-acceptance-criteria.md:284-305`, `src/adapters/websocket/hub.go:25-31`, `src/adapters/websocket/hub.go:63-69`, `src/adapters/websocket/hub.go:79-80`, `src/adapters/websocket/handler.go:13-22` |
| Status | OPEN |

**Evidence**

The current docs/specs promise or imply:

- connection identity as `connectionID + userID`
- `UpgradeHandler(hub)` style integration with origin/subprotocol handling
- configurable heartbeat timeout
- `authFunc`
- `maxConnsPerUser`

But the real API only provides:

- `Conn{ID, conn}`
- `HubConfig{PingInterval, ReadLimit}`
- `UpgradeConfig{AllowedOrigins}`
- `MessageHandler func(ctx, connID, data)`

There is no user metadata, no auth hook, no subprotocol surface, and no per-user admission control.

**Impact**

Callers following the published contract will either write integration code that does not compile or assume safety/identity features exist when they do not. This is especially misleading for teams trying to implement authenticated push or per-user/device routing on top of the adapter.

**Recommendation**

Either:

1. implement the documented controls and identity surface, or
2. immediately narrow the specs/docs/acceptance text to the smaller, actual API surface.

Do not keep the current mixed contract.

---

## F-07: Tests miss the risky lifecycle and boundary paths

| Field | Value |
|---|---|
| Seat | S3 Test |
| Severity | **P2** |
| Category | Test Coverage |
| Files | `src/adapters/websocket/hub_test.go:54-69`, `src/adapters/websocket/hub_test.go:223-243`, `src/adapters/websocket/hub_test.go:251-260`, `src/adapters/websocket/integration_test.go:11-30` |
| Status | OPEN |

**Evidence**

- `TestHub_StartStop()` covers shutdown with zero live connections only.
- `TestUpgradeHandler_AllowedOrigins()` only asserts that a handler value is non-nil; it never exercises an Origin-allowed vs Origin-rejected handshake.
- `TestHub_RegisterUnregister()` never verifies the unregister path or post-close cleanup.
- All integration tests are `t.Skip(...)` placeholders.

**Impact**

The current suite would not catch the two highest-risk issues in this module: live-connection shutdown hangs and origin-policy regressions.

**Recommendation**

- Add a live-connection shutdown test that proves `Stop(ctx)` returns within a bounded time and drains connections.
- Add handshake tests for allowed and rejected origins.
- Turn at least one integration test into a real end-to-end WebSocket lifecycle test instead of a stub.

---

## Review Conclusion

`adapters/websocket` is close to usable for simple demos, but it is **not yet production-safe as a long-lived push adapter**. The lifecycle bug in `Stop()`, the insecure zero-value origin policy, the reader/handler coupling, and the lack of backpressure handling on broadcast should be treated as the priority fixes before upper-layer device/order WebSocket usage grows.
