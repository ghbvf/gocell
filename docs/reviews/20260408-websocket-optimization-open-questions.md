# WebSocket Optimization and Open Questions

> Date: 2026-04-08
> Status: Draft
> Scope: `src/runtime/websocket/*`, `src/adapters/websocket/*`, spec alignment, test/debt follow-up
> Basis: source review, existing tests, `docs/tech-debt-registry.md`

## 1. Current Baseline

The WebSocket module is split into two layers:

- `src/runtime/websocket/*` owns the Hub lifecycle, connection registry, ping loop, broadcast/send API, and the `Conn` abstraction.
- `src/adapters/websocket/*` binds the runtime contract to `nhooyr.io/websocket` and exposes `UpgradeHandler`.

Current strengths:

1. The runtime/adapter boundary is clean: runtime depends on `Conn`, not on `nhooyr.io/websocket` directly (`src/runtime/websocket/conn.go:5-27`, `src/adapters/websocket/conn.go:17-80`).
2. The Hub lifecycle is explicitly modeled with atomic states and a single shutdown path (`src/runtime/websocket/hub.go:13-27`, `src/runtime/websocket/hub.go:140-277`).
3. Current package tests are green: `go test ./runtime/websocket ./adapters/websocket`.

Current limitation:

- The module still looks more like a reusable infrastructure primitive than a fully productized WebSocket gateway. Several product-facing semantics remain outside the code or are only partially reflected in the spec (`specs/feat/002-phase3-adapters/spec.md:87-94`).

## 2. Pending Optimizations

This section captures the items that can be scheduled as concrete follow-up work.

### 2.1 Immediate Hardening

#### WS-OPT-01: Make origin policy safe by default

- Priority: High
- Category: Security / Boundary
- Evidence:
  - `src/adapters/websocket/handler.go:34-39`
- Problem:
  - When `AllowedOrigins` is empty, the handler explicitly enables `InsecureSkipVerify`, which turns the zero-value configuration into "accept all origins".
- Recommended action:
  1. Keep the library default origin checks when no allowlist is configured.
  2. If a dev-only permissive mode is still needed, expose it as an explicit flag instead of the default path.
  3. Add allow/deny handshake tests.
- Acceptance:
  - Empty config is safe-by-default.
  - Cross-origin behavior is explicitly configured, not implicit.

#### WS-OPT-02: Add handshake identity binding and align with spec

- Priority: High
- Category: Product / Security / Contract
- Evidence:
  - Spec says connections are identified by `connectionID + userID` (`specs/feat/002-phase3-adapters/spec.md:91-94`).
  - Runtime currently indexes only by `conn.ID()` (`src/runtime/websocket/hub.go:306-316`, `src/runtime/websocket/hub.go:423-433`).
  - Upgrade path currently generates only a random `connID` (`src/adapters/websocket/handler.go:52-57`).
- Problem:
  - The code has no built-in user/session binding model, so any auth/routing semantics must be implemented ad hoc by callers.
- Recommended action:
  1. Decide whether user identity belongs in the base Hub or in a higher-level gateway wrapper.
  2. If it belongs here, extend the registration model to carry `userID` and possibly `sessionID` or `deviceID`.
  3. Bind identity during handshake rather than reconstructing it later from app-specific side channels.
- Acceptance:
  - The connection identity model is explicit and consistent across spec, code, and calling cells.

#### WS-OPT-03: Contain `MessageHandler` panic/blocking risk

- Priority: High
- Category: Correctness / Resilience
- Evidence:
  - `readLoop` reads frames and calls `h.handler` inline on the same goroutine (`src/runtime/websocket/hub.go:452-463`).
- Problem:
  - A slow handler blocks the only reader for that connection.
  - A panic in the handler can escape the goroutine boundary and take down the process.
- Recommended action:
  1. Add a `recover` boundary around handler execution.
  2. Decide whether reads should hand off to a bounded work queue instead of running business logic inline.
  3. If inline handling remains, document the latency/panic contract clearly.
- Acceptance:
  - Handler failures are contained to the offending connection or request path.
  - The chosen delivery model is documented and tested.

#### WS-OPT-04: Align documented defaults and implementation behavior

- Priority: High
- Category: Spec / Maintenance
- Evidence:
  - Code default ping timeout is `5s` (`src/runtime/websocket/hub.go:22-27`).
  - Spec documents WebSocket pong timeout as `10s` (`specs/feat/002-phase3-adapters/spec.md:451-452`).
  - Spec also mentions signal-first mode and subprotocol negotiation (`specs/feat/002-phase3-adapters/spec.md:91-94`), but these are not enforced or exposed in the current API (`src/runtime/websocket/hub.go:396-433`, `src/adapters/websocket/handler.go:14-25`).
- Problem:
  - The module has already drifted from the documented contract.
- Recommended action:
  1. Pick the intended defaults and update either code or spec.
  2. Mark which behaviors are guaranteed by the framework versus merely recommended usage.
- Acceptance:
  - Spec and implementation no longer disagree on timeout values or feature surface.

### 2.2 Short-Term Productization

#### WS-OPT-05: Support explicit subprotocol negotiation or remove it from the contract

- Priority: Medium
- Category: Protocol / Product
- Evidence:
  - Spec requires subprotocol negotiation (`specs/feat/002-phase3-adapters/spec.md:93`).
  - `UpgradeConfig` currently exposes only `AllowedOrigins` (`src/adapters/websocket/handler.go:14-19`).
  - `websocket.AcceptOptions` is created but no subprotocols are configured (`src/adapters/websocket/handler.go:32-41`).
- Problem:
  - The product contract promises a capability that the adapter does not currently expose.
- Recommended action:
  1. Either add subprotocol configuration and negotiation tests,
  2. Or remove subprotocol support from the contract for now.
- Acceptance:
  - Handshake capabilities are explicit and test-covered.

#### WS-OPT-06: Preserve request metadata in per-connection context

- Priority: Medium
- Category: DX / Observability
- Evidence:
  - `Register` creates each connection context from `context.Background()` (`src/runtime/websocket/hub.go:313-316`).
  - `MessageHandler` receives that derived context (`src/runtime/websocket/hub.go:452-463`).
  - Current registry already tracks this as open debt (`docs/tech-debt-registry.md:162-163`, `WS-DX-01`).
- Problem:
  - Request ID, tracing, subject, or handshake metadata do not flow into message handling.
- Recommended action:
  1. Allow registration with a parent context or metadata struct.
  2. Preserve request-derived fields that matter for observability and audit.
- Acceptance:
  - Downstream handlers can correlate a message to the original connection or handshake request.

#### WS-OPT-07: Improve diagnostics for live connections

- Priority: Medium
- Category: DX / Operations
- Evidence:
  - Connection logs mostly emit opaque `conn_id` values (`src/runtime/websocket/hub.go:253-256`, `src/runtime/websocket/hub.go:323-329`, `src/adapters/websocket/handler.go:58-66`).
  - Current registry also flags missing remote-address support (`docs/tech-debt-registry.md:163-164`, `WS-DX-02`).
- Problem:
  - Debugging live connection issues is harder than it needs to be.
- Recommended action:
  1. Decide whether `Conn` should expose remote address or whether metadata should live outside the interface.
  2. Add structured connection metadata to logs where it improves diagnostics without creating cardinality explosions.
- Acceptance:
  - Operators can correlate suspicious or failing connections without relying only on UUIDs.

#### WS-OPT-08: Make write failure behavior explicit

- Priority: Medium
- Category: Correctness / Backpressure
- Evidence:
  - `Conn.Write` uses a hard-coded `10s` timeout (`src/adapters/websocket/conn.go:15`, `src/adapters/websocket/conn.go:63-69`).
  - `Broadcast` logs write failures but does not evict the connection (`src/runtime/websocket/hub.go:406-419`).
  - `Send` returns the error but leaves cleanup policy to the caller (`src/runtime/websocket/hub.go:423-433`).
- Problem:
  - The system has no explicit policy for when a write-failing or timing-out connection should be removed.
- Recommended action:
  1. Decide whether deterministic write timeouts should trigger `Unregister`.
  2. Make write timeout configurable if it remains part of the runtime contract.
- Acceptance:
  - Slow or broken downstream clients do not linger indefinitely without a clear policy.

### 2.3 Scale and Operational Robustness

#### WS-OPT-09: Prepare broadcast fan-out for higher connection counts

- Priority: Medium
- Category: Performance / Operations
- Evidence:
  - `Broadcast` snapshots connections and spawns one goroutine per connection before waiting for all sends to finish (`src/runtime/websocket/hub.go:398-419`).
- Problem:
  - This is fine at small scale, but becomes expensive once connection counts grow.
- Recommended action:
  1. Keep the current design for small deployments if the expected scale is low.
  2. If higher scale is expected, move to bounded worker pools, per-connection send queues, or a sharded hub model.
- Acceptance:
  - Broadcast latency and goroutine count remain bounded at the target scale.

#### WS-OPT-10: Make ping sweep strategy scale-aware

- Priority: Medium
- Category: Performance / Health Checking
- Evidence:
  - `pingAll` snapshots the map and then pings each connection sequentially (`src/runtime/websocket/hub.go:480-525`).
  - The default ping interval is `30s` and timeout is `5s` (`src/runtime/websocket/hub.go:22-27`, `src/runtime/websocket/hub.go:47-53`).
- Problem:
  - Sequential sweeps can become long-running as connection counts grow, which makes health-check cost linear in connection count.
- Recommended action:
  1. Keep the current model if the target scale stays modest.
  2. Otherwise use bounded parallelism or sharding so the ping sweep does not become a serial bottleneck.
- Acceptance:
  - Ping health checks do not dominate CPU time or exceed the configured sweep budget at the intended scale.

#### WS-OPT-11: Make shutdown tuning explicit

- Priority: Medium
- Category: Operations / Config
- Evidence:
  - External-cancel shutdown uses a hard-coded `10s` timeout (`src/runtime/websocket/hub.go:26`, `src/runtime/websocket/hub.go:178-183`).
  - Tech-debt already tracks this (`docs/tech-debt-registry.md:159-160`, `WS-OPS-01`).
  - Current shutdown also closes entries synchronously one by one (`src/runtime/websocket/hub.go:241-258`), with a note that high connection counts may later need concurrent close (`docs/tech-debt-registry.md:160-161`, `WS-OPS-02`).
- Problem:
  - Timeout and close strategy are currently implementation details instead of deployment knobs.
- Recommended action:
  1. Add `ShutdownTimeout` to `HubConfig` if operations need to tune it.
  2. Keep synchronous close for now if scale is small; switch to concurrent close when scale data justifies it.
- Acceptance:
  - Stop behavior is predictable and tunable in the environments that run it.

### 2.4 Test and Regression Coverage

#### WS-OPT-12: Add missing lifecycle, protocol, and regression tests

- Priority: High
- Category: Testing / Confidence
- Evidence:
  - Tech-debt already lists missing `Stop + external cancel` concurrency coverage and stopped-hub send/broadcast coverage (`docs/tech-debt-registry.md:156-158`, `WS-T-01`, `WS-T-02`).
  - Current tests cover many lifecycle cases (`src/runtime/websocket/hub_test.go:145-230`, `src/runtime/websocket/hub_test.go:424-478`, `src/runtime/websocket/hub_test.go:761-949`) but still do not verify:
    - origin allow/deny behavior,
    - read limit rejection behavior,
    - subprotocol negotiation,
    - handler panic/blocking containment,
    - stopped-hub `Broadcast` semantics.
  - Adapter tests still guard around local TCP availability (`src/adapters/websocket/handler_test.go:37-43`).
- Recommended action:
  1. Add explicit allow/deny origin tests.
  2. Add at least one read-limit enforcement test against the real adapter.
  3. Add `Stop` vs external-cancel concurrency coverage.
  4. Add stopped-hub `Send/Broadcast` expectation tests.
  5. If `runtime/http` middleware changes continue, add one full router -> middleware -> upgrade regression test.
- Acceptance:
  - The highest-risk boundary and lifecycle behaviors are locked down by tests, not just by comments.

## 3. Open Questions

These questions should be answered before some of the optimizations above are implemented, otherwise the code may be refactored in the wrong direction.

### WS-Q-01: Is this module a generic runtime Hub or a product WebSocket gateway?

- Why it matters:
  - A generic Hub should stay minimal and avoid baking in auth, routing, and message semantics.
  - A product gateway should own identity, protocol versioning, and signal conventions.
- Relevant evidence:
  - The runtime API is generic (`src/runtime/websocket/conn.go:12-27`, `src/runtime/websocket/hub.go:396-433`).
  - The spec is more product-opinionated (`specs/feat/002-phase3-adapters/spec.md:91-94`).

### WS-Q-02: What is the canonical connection identity model?

- Options to settle:
  - `connID` only
  - `userID + connID`
  - `userID + sessionID`
  - `userID + deviceID + connID`
- Relevant evidence:
  - The spec already assumes user-aware identity (`specs/feat/002-phase3-adapters/spec.md:91`).
  - The implementation does not (`src/runtime/websocket/hub.go:306-316`, `src/adapters/websocket/handler.go:54-57`).

### WS-Q-03: Which authentication modes must the handshake support?

- Options to settle:
  - anonymous/internal only
  - bearer token
  - cookie-backed browser session
  - signed URL / service token
- Why it matters:
  - Origin policy, identity binding, and logging requirements change materially depending on the auth mode.

### WS-Q-04: Are browser clients a first-class production use case?

- Why it matters:
  - If yes, strict origin validation must be the default.
  - If no, the adapter can optimize for server-to-server or internal tooling clients.
- Relevant evidence:
  - Current zero-value origin behavior is permissive (`src/adapters/websocket/handler.go:34-39`).

### WS-Q-05: Does the product need negotiated subprotocols or only one internal protocol?

- Why it matters:
  - This decides whether the adapter should expose protocol version negotiation now or avoid complexity until multiple clients exist.

### WS-Q-06: What scale target should the current node design optimize for?

- Options to settle:
  - tens of connections
  - hundreds
  - low thousands
  - 10k+
- Why it matters:
  - It directly affects whether the current broadcast, ping, and shutdown implementations are sufficient (`src/runtime/websocket/hub.go:241-258`, `src/runtime/websocket/hub.go:398-419`, `src/runtime/websocket/hub.go:480-525`).

### WS-Q-07: What outbound delivery semantics are required?

- Questions to settle:
  - Is best-effort delivery enough?
  - Must messages remain ordered per connection?
  - Should slow clients be dropped, skipped, or backpressured?
- Why it matters:
  - The answer determines whether inline writes are acceptable or whether per-connection queues are required.

### WS-Q-08: Should signal-first be enforced by framework code or left to application discipline?

- Relevant evidence:
  - The spec says the Hub should default to lightweight refresh signals (`specs/feat/002-phase3-adapters/spec.md:92`).
  - The runtime API accepts arbitrary `[]byte` payloads and does not enforce message shape (`src/runtime/websocket/hub.go:396-433`).
- Why it matters:
  - If signal-first is a real product invariant, the runtime should probably own part of the message envelope or publishing API.

### WS-Q-09: What observability contract is required for each live connection?

- Questions to settle:
  - Do we need request ID / trace ID propagation?
  - Do we need remote address, user ID, auth method, and disconnect reason in logs?
  - Do we need active-connection, ping-failure, and send-failure metrics?
- Why it matters:
  - It determines whether the current `Conn` interface and per-connection context are sufficient (`src/runtime/websocket/conn.go:12-27`, `src/runtime/websocket/hub.go:313-316`).

## 4. Suggested Execution Order

If this module is actively moving toward production usage, the recommended order is:

1. `WS-OPT-01` safe-by-default origin policy.
2. `WS-Q-01` through `WS-Q-05` on module role, identity, auth, browser scope, and subprotocols.
3. `WS-OPT-02` and `WS-OPT-04` to align identity/contract/spec.
4. `WS-OPT-03` to contain handler failures.
5. `WS-OPT-12` to lock the intended behavior with regression tests.
6. `WS-Q-06` and `WS-Q-07` on scale and delivery semantics.
7. `WS-OPT-08` through `WS-OPT-11` based on the answers above.

## 5. Existing Registry Cross-References

The following open entries in `docs/tech-debt-registry.md` remain directly relevant:

- `WS-T-01`: missing `Stop + external cancel` concurrent-shutdown test.
- `WS-T-02`: missing stopped-hub `Broadcast/Send` behavior tests.
- `WS-OPS-01`: external-cancel shutdown timeout is hard-coded.
- `WS-OPS-02`: shutdown close path may need concurrency at larger scale.
- `WS-DX-01`: per-connection context currently drops tracing/correlation fields.
- `WS-DX-02`: missing remote-address level diagnostics.

This document expands those entries with current code evidence and adds the open product decisions that still need explicit ownership.
