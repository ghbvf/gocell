# R1D-6: adapters/websocket Independent Review

| Field | Value |
|---|---|
| Mode | Independent pass |
| Scope | `adapters/websocket/` |
| Review basis commit | `b6b14af` |
| Date | 2026-04-06 |

---

## Executive Summary

This independent pass reviewed `adapters/websocket` from source and tests only.

The module is compact, but it still has several production-facing risks around shutdown, upgrade boundary safety, and long-lived connection handling. I found **4 P1** and **2 P2** findings.

| Severity | Count | Summary |
|---|---:|---|
| P0 | 0 | -- |
| P1 | 4 | shutdown self-deadlock, insecure zero-value origin policy, handler panic/liveness coupling, broadcast HOL blocking |
| P2 | 2 | lifecycle/config robustness gaps, critical-path tests missing |

---

## F-01: `Stop()` can deadlock when active connections exist

| Field | Value |
|---|---|
| Severity | **P1** |
| Category | Lifecycle / Availability |
| Files | `adapters/websocket/handler.go:44-47`, `adapters/websocket/hub.go:132-155`, `adapters/websocket/hub.go:176-181`, `adapters/websocket/hub.go:250-258` |
| Status | OPEN |

**Evidence**

- `UpgradeHandler` registers connections with `context.Background()`.
- `Register()` adds each `readLoop()` goroutine to `h.wg`.
- `Stop()` first calls `h.wg.Wait()`, and only after that closes active connections.
- `readLoop()` exits only when `Read()` returns or its context is cancelled.

With a live client:

1. `readLoop()` blocks in `conn.Read(backgroundCtx)`.
2. `Stop()` waits for `readLoop()` to exit.
3. `Stop()` never reaches the connection close path that would unblock `Read()`.

**Impact**

If the service is shutting down while any client is still connected, graceful shutdown can hang indefinitely. That breaks rolling restart and can force external kill instead of orderly drain.

**Recommendation**

- Bind read loops to a Hub-owned cancellable lifecycle context.
- In `Stop(ctx)`, stop accepting new work and close/unregister active connections before waiting.
- Honor the caller's `ctx` instead of discarding it.

---

## F-02: Zero-value upgrade config disables same-origin protection

| Field | Value |
|---|---|
| Severity | **P1** |
| Category | Security / Boundary Validation |
| File | `adapters/websocket/handler.go:26-30` |
| Status | OPEN |

**Evidence**

When `AllowedOrigins` is empty, the handler sets `AcceptOptions.InsecureSkipVerify = true`.

That means the zero-value path does not fall back to a safe default; it explicitly disables origin verification.

**Impact**

The default adapter boundary accepts cross-site WebSocket upgrades from arbitrary origins. If a caller later adds cookie-based auth or browser-attached credentials, this becomes an easy footgun for cross-site WebSocket hijacking.

**Recommendation**

- Keep library default origin checks when `AllowedOrigins` is empty.
- If an insecure dev mode is needed, expose it as an explicit opt-in flag rather than the default path.

---

## F-03: `MessageHandler` runs inline on the sole reader goroutine

| Field | Value |
|---|---|
| Severity | **P1** |
| Category | Concurrency / Resilience |
| Files | `adapters/websocket/hub.go:79-80`, `adapters/websocket/hub.go:250-264`, `adapters/websocket/hub.go:283-299` |
| Status | OPEN |

**Evidence**

- `readLoop()` directly invokes the injected `MessageHandler`.
- There is no `recover` around that callback.
- The same module uses `Ping()`-based liveness checks and removes connections on timeout.

This creates two independent hazards:

1. a panic inside the handler can escape the goroutine and crash the whole process;
2. a slow handler blocks the only reader, so pong processing is delayed and healthy connections can be dropped as false failures.

**Impact**

One bad message or one slow downstream handler can destabilize the whole WebSocket adapter, either by taking down the process or by turning normal clients into false timeouts.

**Recommendation**

- Decouple socket reads from business handling with a queue/worker handoff.
- Add `recover` at the handler boundary and close only the offending connection on failure.
- Keep the reader goroutine focused on frame I/O while ping/pong health checks are enabled.

---

## F-04: Broadcast fan-out is serial and slow clients amplify latency

| Field | Value |
|---|---|
| Severity | **P1** |
| Category | Performance / Availability |
| Files | `adapters/websocket/hub.go:18-19`, `adapters/websocket/hub.go:33-48`, `adapters/websocket/hub.go:209-225` |
| Status | OPEN |

**Evidence**

- `Broadcast()` copies all connections, then writes to them one by one.
- `Conn.Write()` wraps each send with a fixed `10s` timeout.
- Failed writes are logged, but the bad connection is not immediately removed.

**Impact**

A single slow or half-dead client can delay delivery to every later client in the loop. With multiple bad clients, latency stacks linearly. That is especially risky for a signal-first push adapter where timeliness is the main value.

**Recommendation**

- Move to bounded concurrent fan-out or per-connection send queues.
- Immediately unregister connections after deterministic write timeout/failure.
- Make write timeout configurable if the serial model remains.

---

## F-05: Lifecycle/config robustness is incomplete

| Field | Value |
|---|---|
| Severity | **P2** |
| Category | Robustness / State Management |
| Files | `adapters/websocket/hub.go:97-110`, `adapters/websocket/hub.go:113-128`, `adapters/websocket/hub.go:132-137`, `adapters/websocket/hub.go:268-270` |
| Status | OPEN |

**Evidence**

- `NewHub()` only replaces zero values; it does not reject invalid negative `PingInterval`.
- `pingLoop()` passes `PingInterval` directly to `time.NewTicker`, so a negative value will panic at runtime.
- `Start()` overwrites `h.cancel` every time it is called.
- `Stop()` uses `sync.Once` for cancellation, but there is no explicit lifecycle state to guard repeated or out-of-order `Start/Stop` calls.

**Impact**

Bad config can crash startup, and unusual call ordering can leave the Hub in an undefined lifecycle state that is difficult to reason about or safely reuse.

**Recommendation**

- Validate config eagerly in `NewHub()`.
- Add explicit lifecycle state and make repeated/illegal transitions deterministic.
- Expose timing knobs that matter to production behavior instead of hardcoding them.

---

## F-06: Critical shutdown and origin paths are not really tested

| Field | Value |
|---|---|
| Severity | **P2** |
| Category | Test Coverage |
| Files | `adapters/websocket/hub_test.go:54-69`, `adapters/websocket/hub_test.go:223-243`, `adapters/websocket/hub_test.go:251-260`, `adapters/websocket/integration_test.go:11-30` |
| Status | OPEN |

**Evidence**

- `TestHub_StartStop()` only covers a Hub with no live connections.
- `TestUpgradeHandler_AllowedOrigins()` only checks that a handler object exists; it does not verify allow/deny behavior.
- `TestHub_RegisterUnregister()` never verifies the unregister path.
- All integration tests are stubs with `t.Skip(...)`.

**Impact**

The current suite would not catch the highest-risk regressions in this module: shutdown hangs with active clients and origin-policy mistakes.

**Recommendation**

- Add a live-connection shutdown test with a bounded timeout.
- Add real origin allow/deny handshake tests.
- Replace at least one integration stub with a real end-to-end lifecycle test.

---

## Conclusion

`adapters/websocket` is fine for a minimal demo, but it is not yet safe enough for production long-lived connections. The first fixes should be:

1. make shutdown non-blocking with active clients,
2. restore a safe default origin policy,
3. decouple handler execution from the reader goroutine,
4. remove broadcast head-of-line blocking.
