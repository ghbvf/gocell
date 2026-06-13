# ADR: Webhook dispatcher circuit breaker — per-endpoint full state machine

- Status: Accepted
- Date: 2026-06-14
- Context: KERNEL-WEBHOOK-01 follow-up (#1541), follow-up to PR-6 closeout (#1538)
- Supersedes / amends: amends `202606012052-1160-adr-webhook-retry-default.md`
  (adds a fast-fail gate in front of the retry schedule); reclassifies the
  circuit breaker primitive from adapter to kernel.

## Context

The outbound webhook dispatcher (`kernel/webhook.Dispatcher`) delivered every
queued [outbox.Entry] with a real HTTP POST and only the simplified resilience
of the retry schedule: a delivery fails → it is Requeued → the broker redelivers
per the Svix schedule (5s / 5min / 30min / 2h / 5h / 10h / 10h) → after
`MaxRetries` the outbox Rejects it to the DLX (ADR 1160 §D1/D2).

That means a **down endpoint is hammered**: every redelivery in the ~40h retry
window opens a TCP connection and waits up to `defaultDeliveryTimeout` (30s) for
a target that is known to be failing. #1541 asks for the classic circuit-breaker
full state machine (Closed → Open → HalfOpen) so an unhealthy endpoint is
**fast-failed** instead of repeatedly POSTed.

A complete generation+expiry state machine already existed in
`adapters/circuitbreaker` (sony/gobreaker model), built for the inbound
`runtime/http/middleware.CircuitBreaker` but **never wired** (no non-test
importer). The webhook `Dispatcher` lives in `kernel/webhook` and, by the layer
rules, cannot import `adapters/`.

## Decision

### D1 — Reclassify the circuit breaker as a kernel primitive

A circuit breaker is a **pure algorithmic resilience primitive** (clock-only, no
external infrastructure). It does not belong in `adapters/` (which adapts
external systems). We move the state machine to `kernel/circuitbreaker`
(`Breaker` / `Config` / `State` / `Counts`, clock-injected) and **delete the
`adapters/circuitbreaker` go.work module** — a thin re-export wrapper of a
dormant, unwired adapter would be a forbidden parallel structure.

Two consumers now share the one machine:

- `kernel/webhook` gates outbound delivery per endpoint (this ADR).
- `runtime/http/middleware.CircuitBreaker` accepts a `*circuitbreaker.Breaker`
  as its `Allower` — the `Allow()/done(err)` two-step protocol and `RetryAfter`
  structurally satisfy `middleware.Allower` / `CircuitBreakerRetryAfter` (kernel
  does not import runtime; the match is pinned by a compile-time assertion in
  the middleware test).

### D2 — Per-endpoint breaker, gated before the HTTP attempt

`Dispatcher` holds a `circuitGate`: a bounded `map[targetURL]*Breaker`. `Handle`
resolves + SSRF-vets + signs the request (unchanged), then calls
`circuitGate.Allow(req.URL.String())` **before** `client.Do`:

- **Closed / HalfOpen-with-slot** → allowed: POST, then report the outcome.
- **Open / HalfOpen-budget-exhausted** → fast-fail: **no HTTP attempt**, return
  `outbox.Requeue(ErrCircuitOpen)`, record `webhook_deliveries_total{result=circuit_open}`.

Per-endpoint keying isolates failures: one unhealthy target does not fast-fail
deliveries to healthy ones.

### D3 — Failure classification tracks endpoint HEALTH, not payload validity

The single `circuitProbeOutcome` predicate decides what counts as a breaker
failure (`done(err)`):

| Delivery outcome | Breaker | Rationale |
|------------------|---------|-----------|
| transport fault (timeout / conn refused / DNS) | **failure** | endpoint unreachable |
| 5xx | **failure** | endpoint broken / overloaded |
| 429 | **failure** | endpoint throttling |
| 2xx | success | healthy |
| other 4xx (400/401/404/410…) | success | endpoint **is up**, just rejecting this payload |
| SSRF-blocked / permanent prepare-reject | not counted | config/policy issue, not an unhealthy endpoint |

This is **orthogonal to the outbox disposition**: `Classify` still Requeues every
non-2xx (ADR 1160 §D2). The breaker only decides whether to *attempt* the POST;
`Classify` decides what to do with the *result*. A steady stream of 404s keeps
the circuit closed (and keeps Requeuing) — the breaker never mistakes payload
rejection for an outage. SSRF blocks reach `done` only on the rare dial-time
path (pre-flight SSRF rejects never enter the gate); they are reported as
success so a misconfigured target cannot trip the breaker into hiding a genuine
Reject→DLX signal.

### D4 — Defaults, always-on, no v1 configuration knob

The breaker is **enabled by default** with the sony/gobreaker-standard defaults,
stated explicitly in `kernel/webhook/circuit.go`:

- trip on consecutive failures **> 5**;
- open timeout **60s** (then a single half-open probe);
- half-open probe budget **1**.

There is no production opt-out (a disabled breaker would be the banned
noop-publisher posture). Per-deployment / per-contract threshold tuning is a
genuine future need, not built speculatively — backlogged (cf. #1542
per-contract Claim TTL).

### D5 — Composition with the outbox DLX (terminal behavior unchanged)

The breaker **does not change the terminal DLX path**. A permanently-down
endpoint still exhausts `MaxRetries` and is Rejected to the DLX. Because the Svix
`BrokerDelaySchedule` spaces redeliveries on the same timeline whether each
attempt does a POST or fast-fails, **time-to-DLX does not regress** — the breaker
only removes the wasted HTTP round-trips + 30s timeouts within the retry window.

### D6 — In-memory, per-process scope (no distributed breaker)

Breaker state is per-process in-memory, like every classic circuit breaker
(sony/gobreaker, resilience4j). In a multi-pod deployment each pod trips
independently — acceptable and standard: a circuit breaker is a local
availability optimization, not a cluster-coordinated control. We deliberately do
**not** build a distributed (Redis-backed) breaker; the cost/complexity is not
justified, and the outbox DLX remains the cross-pod correctness backstop.

## Enforcement & AI-robustness

| Mechanism | Grade |
|-----------|-------|
| `circuitbreaker.State` sealed enum (closed value set, exhaustive `String`) | Hard (type system) |
| Gate sits in the sole `Handle` → sole `client.Do` egress, already funneled by `WEBHOOK-SSRF-GUARD-01` → bypass is structurally inexpressible | Hard (no new guard needed) |
| `webhook_deliveries_total{result}` value set incl. `circuit_open` frozen by `WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01` (+ negative control) | Medium |
| `ErrCircuitBreakerConfig` / `ErrCircuitOpen` under owned `ERR_CIRCUIT_` prefix, `ERRCODE-PREFIX-OWNERSHIP-01` golden | Hard (golden) |

No new archtest is introduced: the State enum is already type-sealed and the gate
inherits the existing SSRF egress funnel, so an additional `WEBHOOK-CIRCUIT-*`
scan would only duplicate coverage.

## Consequences

- Down endpoints stop consuming HTTP connections + delivery-timeout budget during
  their retry window; operators see `result=circuit_open` and breaker
  `state transition` slog events.
- `adapters/circuitbreaker` is gone; the dormant inbound-HTTP `WithCircuitBreaker`
  wiring now consumes `kernel/circuitbreaker` directly.
- A pathological selector fanning out to >1024 distinct target URLs triggers
  bounded LRU-style eviction in the gate (documented safety valve; a re-observed
  endpoint rebuilds its breaker).

## References

- ADR 1160 — webhook retry defaults (this ADR amends it): `202606012052-1160-adr-webhook-retry-default.md`
- ADR — kernel clock injection (PROD-CLOCK-INJECTION-01): `202605021500-adr-kernel-clock-injection.md`
- Implementation: `kernel/circuitbreaker/`, `kernel/webhook/circuit.go`, `kernel/webhook/dispatcher.go`
- ref: sony/gobreaker v2 — generation+expiry state machine, Allow/done two-step protocol
