# ADR 202605191500 — metrics.Provider.GaugeVec Funnel (METRICS-GAUGEVEC-FUNNEL-01)

**Status**: accepted
**Date**: 2026-05-19
**Author**: D3a-1 (plan 030 R-01 GaugeVec wave)

---

## Context

D3a-1 W1–W7 added `kernel/observability/metrics.Provider.GaugeVec` and five new
metric families used by the event router and outbox consumer subsystems:

| Metric | Type | Labels |
|--------|------|--------|
| `event_router_subscriptions_active` | Gauge | `{cell}` |
| `event_router_setup_errors_total` | Counter | `{cell,topic,reason}` |
| `event_router_ready_wait_seconds` | Histogram | `{cell}` |
| `event_router_runtime_errors_total` | Counter | `{cell,topic,reason}` |
| `outbox_pending_depth` | Gauge | `{cell}` |
| `outbox_consumer_rejected_total` | Counter | `{cell,topic,reason}` |

`event_router_runtime_errors_total` was added in the Wave 2 fix-up batch (PR #589 follow-ups).
After PR #593 review fix-up (P2#3) the `reason` label set is partitioned by
phase, not by error variety:

- `event_router_setup_errors_total.reason` ∈ {`setup_error`, `panic`,
  `ready_timeout`, `subscribe_failure`} — Phase 1–3 failures
- `event_router_runtime_errors_total.reason` ∈ {`runtime_fault`} — Phase 4
  faults only

Before D3a-1 there was no `GaugeVec` method on `metrics.Provider`.  Ad-hoc code
could call `prometheus.NewGaugeVec` or either of the OTel Meter gauge
primitives (`Float64UpDownCounter` historically, `Float64Gauge` since
PR #625) directly, bypassing the kernel interface.  This creates two
problems:

1. The Prometheus adapter uses `registerOrReuse` to avoid duplicate-registration
   panics; direct callers skip that safety layer.
2. The OTel adapter wraps `Float64Gauge.Record` in a per-label-set last-value
   slot (LastValue semantics for `Set/Inc/Dec/Add`); direct callers bypass the
   slot and lose the cumulative semantics emulation.

W8 adds archtest `METRICS-GAUGEVEC-FUNNEL-01` to enforce that no business
package calls these banned constructors directly. PR #593 review fix-up (P2#4)
extends the OTel ban set to cover both `Float64UpDownCounter` (the historical
leak surface) and `Float64Gauge` (the current adapter primitive).

---

## Decision

### Int64ObservableGauge scope boundary

`Int64ObservableGauge` (an Observable / callback-based instrument in the OTel
API) is **NOT** in the banned constructor set enforced by `METRICS-GAUGEVEC-FUNNEL-01`.

Rationale:
- Observable instruments are registered via a `RegisterCallback` on the
  `MeterProvider` and fire at collection time, not at call time.  They are
  architecturally distinct from the synchronous gauge primitives
  (`Float64UpDownCounter` / `Float64Gauge`) used by `otelGaugeVec.Set/Inc/Dec/Add`.
- The current `MetricProvider.GaugeVec` implementation routes through
  `Float64Gauge.Record` (OTel v1.33+ synchronous gauge, LastValue semantics),
  not through any Observable pattern.  The archtest rule bans the two
  synchronous primitives that `Provider.GaugeVec` replaces; banning
  `Int64ObservableGauge` would over-reach into a different instrument class
  that has no current Provider-level replacement.
- If a future PR introduces Observable-based gauge usage that should route
  through `MetricProvider`, a **new** funnel design (separate ADR + archtest
  rule extension) is required at that time.  `METRICS-GAUGEVEC-FUNNEL-01` must
  not be silently extended to cover Observable instruments without an explicit
  scope amendment.

### Interface shape

`metrics.Provider.GaugeVec(opts GaugeOpts) (GaugeVec, error)` exposes four
operations:

| Method | Prometheus | OTel |
|--------|-----------|------|
| `Set(v float64)` | `prom.Gauge.Set` | delta = v − last; UpDownCounter.Add(delta) |
| `Inc()` | `prom.Gauge.Inc` | UpDownCounter.Add(+1) |
| `Dec()` | `prom.Gauge.Dec` | UpDownCounter.Add(−1) |
| `Add(delta float64)` | `prom.Gauge.Add` | UpDownCounter.Add(delta) |

`Sub` is intentionally absent: `Sub(x)` is equivalent to `Add(-x)` and its
absence reduces the interface surface.  `SetToCurrentTime` is absent because it
encodes a wall-clock assumption that belongs in the caller, not the metric
primitive.

### Prometheus adapter (`adapters/prometheus`)

`MetricProvider.GaugeVec` calls `prom.NewGaugeVec` then routes through
`registerOrReuse[*prom.GaugeVec]`, matching the existing CounterVec/HistogramVec
pattern.  Duplicate-name registrations return the existing collector wrapped in a
fresh `promGaugeVec`; any other error surfaces as `ErrAdapterPromRegister`.

### OTel adapter (`adapters/otel`)

`MetricProvider.GaugeVec` calls `meter.Float64Gauge` (OTel v1.33+ synchronous
gauge) and wraps the result in `otelGaugeVec`.  `Set(v)` calls `Record(v)`
directly — `Float64Gauge.Record` takes an absolute value with LastValue export
semantics, so no delta arithmetic is needed.  `Inc/Dec/Add` still maintain a
per-label-set last-value slot protected by `sync.Mutex` to compute the new
absolute value:

```
Inc:  last++; Record(ctx, last, attrs)
Dec:  last--; Record(ctx, last, attrs)
Add:  last += delta; Record(ctx, last, attrs)
```

Each `With(labels)` call returns the same `*otelGauge` for a given label tuple
so concurrent callers sharing the same label set operate on the same
last-value slot, making the cumulative semantics correct.

ref: opentelemetry-go `metric.Meter.Float64Gauge` (v1.33+) — synchronous gauge
with LastValue / `metricdata.Gauge` export semantics.
ref: prometheus/client_golang `prometheus/gauge.go` — `NewGaugeVec` is the
standard constructor routed through `registerOrReuse`.

---

## Funnel 双向锁评级

| Side | Grade | Mechanism |
|------|-------|-----------|
| Downstream | **Hard** | Callee resolved via `*types.Info.Uses` (package-level func) and `*types.Info.Selections` (interface method) to `github.com/prometheus/client_golang/prometheus.NewGaugeVec` and the OTel ban set `{Float64UpDownCounter, Float64Gauge}` on `go.opentelemetry.io/otel/metric.Meter`; form-uniqueness — no gray zone. The OTel side covers both the historical leak surface and the current adapter primitive (PR #593 review fix-up P2#4). |
| Upstream | **Medium** | `prom.NewGaugeVec` is a public exported function; Go type system cannot prevent import + call in business packages; archtest scope filter (excluding `adapters/prometheus/` and `adapters/otel/`) is the caller-allowlist mechanism |

Backlog entry for upstream Hard upgrade: **`METRICS-GAUGEVEC-UPSTREAM-HARD-01`**
(`docs/backlog/cap-13-observability.md` §13.5).  Upgrade path: wrap both banned
constructors inside `adapters/prometheus/internal/promwrap/` and
`adapters/otel/internal/otelwrap/` (Go `internal` packages) — external import
becomes a compile error.

The archtest godoc in `TestGaugeVecFunnel`
(`tools/archtest/observability_metrics_test.go`) points at this ADR and the
backlog entry by name.

---

## 威胁矩阵

| Threat | Impact | Mitigation | Status |
|--------|--------|------------|--------|
| High-cardinality GaugeVec mis-use (unbounded labels) | OOM / metric explosion | Label sets documented in godoc of each collector; adapters enforce `MustValidateLabels` on every `With()` call; OTel adapter caps at `defaultAttrCacheMaxSize=2000` (matching OTel SDK `defaultCardinalityLimit`) | ✅ bounded by design |
| OTel sync-diff race (concurrent Set/Inc/Dec/Add) | Incorrect last-value, wrong delta emitted | `otelGauge.mu sync.Mutex` serialises all four methods; the OTel SDK `Float64UpDownCounter.Add` itself is goroutine-safe so the only critical section is the read-modify-write on `last` | ✅ mutex-protected per label-set |
| archtest bypass via reflect (`reflect.ValueOf(prom.NewGaugeVec)`) | BS-1 blind spot | `TestGaugeVecFunnel_SelfCheck` BS-1 asserts no production `reflect.ValueOf` call contains the banned symbol name string | ⚠️ accepted Medium risk; production code does not use reflect for metrics |
| archtest bypass via function-value indirection (`var fn = prom.NewGaugeVec`) | BS-3 blind spot | `TestGaugeVecFunnel_SelfCheck` BS-3 asserts no production SelectorExpr resolves to a banned symbol outside a direct call position | ⚠️ accepted Medium risk; production code does not use function-value indirection for metrics |
| archtest bypass via cross-package Registry forwarding | BS-2 blind spot | Out of scope — forwarding a Registry does not itself call NewGaugeVec; legitimate provider-internal usage | ⚠️ accepted; no violation possible via this path |
| Duplicate registration panic (Prometheus) | Bootstrap failure | `registerOrReuse` pattern converts `AlreadyRegisteredError` to safe reuse or `ErrAdapterPromRegister` | ✅ no panic path in adapter |
| Business code calls OTel synchronous gauge primitive directly (`meter.Float64Gauge` or `meter.Float64UpDownCounter`) | GaugeVec funnel bypassed; LastValue semantics + sync.Mutex slot lost | `METRICS-GAUGEVEC-FUNNEL-01` ban set covers BOTH primitives (Wave 3 of PR #593 review fix-up); `bannedOtelMethods` map keyed on method name; BS-1 reflect-string self-check + BS-3 method-value-capture self-check iterate over the full set | ✅ Hard downstream — `*types.Info`-resolved (callee, name) pair against the ban set; form-uniqueness across both primitives |
| Adapter self-misuse: `adapters/otel/` switches to a third OTel gauge primitive (not in ban set) | OTel emulation bypassed in adapter; incorrect cumulative semantics | `adapters/otel/` is the sanctioned implementation site and is excluded from the archtest ban; any switch to a new primitive must be made visible by extending `bannedOtelMethods` in the same PR; regressions are also caught by OTel adapter unit tests (sync-diff correctness) | ⚠️ accepted (adapter is sanctioned implementation site; a primitive switch is a 1-PR amendment — see `bannedOtelMethods` godoc in `tools/archtest/observability_metrics_test.go`) |

---

## 替代方案表

### A. Set method shape

| Option | Description | Decision |
|--------|-------------|----------|
| **Include Set (chosen)** | `metrics.Gauge` exposes `Set/Inc/Dec/Add` | Chosen (D1: needed for `outbox_pending_depth` which emits an absolute count per reclaim tick) |
| Inc/Dec/Add only | No Set; callers must track state and emit deltas | Rejected — `outbox_pending_depth` emits an absolute count; callers would need their own last-value book-keeping, reproducing the OTel adapter logic in every call site |

### B. Pending depth metric source

| Option | Description | Decision |
|--------|-------------|----------|
| **Extend `Store.CountPending` (chosen)** | Relay calls `CountPending` on each tick, emits via `OutboxPendingDepthCollector.ObservePendingDepth`; collector is a split per-metric type (Wave 2: `OutboxConsumerCollector` was split into `OutboxRejectCollector` + `OutboxPendingDepthCollector`, composition root injects per-relay `PendingDepthObserver`) | Chosen (D2: single source of truth; count already available in PG adapter; no in-memory hook needed) |
| In-memory hook | Count maintained in relay memory, no DB query | Rejected — in-memory count diverges from DB truth after crash recovery; adds complexity |

### C. `eventbus_dropped_total` placement

| Option | Description | Decision |
|--------|-------------|----------|
| D3a-1 (this PR) | Add dropped counter in same wave | Rejected (D3: InMemoryEventBus drop path is a separate subsystem; adding it here would widen scope of D3a-1 past GaugeVec + 5 metrics) |
| **D3a-2 (follow-up, chosen)** | Separate wave for InMemoryEventBus drop counter | Chosen — backlog entry R-01 notes this gap; deferred to next wave |

---

## 盲区自检列表

The following production AST forms are NOT covered by `gaugeVecFunnelRule` and
are asserted absent in production by `TestGaugeVecFunnel_SelfCheck`:

| ID | Form not matched | Accepted risk | Self-check |
|----|-----------------|---------------|------------|
| BS-1 | `reflect.ValueOf(prom.NewGaugeVec)` — function value via reflection | Medium; no production code uses reflect for metrics | `gaugeVecBS1ReflectCheck` asserts no `reflect.ValueOf` with banned symbol name string in production |
| BS-2 | Cross-package `*prom.Registry` forwarding followed by internal `NewGaugeVec` — callers forward Registry, not call NewGaugeVec | Out of scope; no violation possible | No self-check needed (structural argument) |
| BS-3 | `var fn = prom.NewGaugeVec; fn(opts, labels)` — function-value indirection | Medium; no production code stores banned symbols in variables | `gaugeVecBS3FuncValueCheck` asserts no SelectorExpr resolving to banned symbol outside direct call position |

---

## ref: framework files

- `prometheus/client_golang/prometheus/gauge.go` — `NewGaugeVec` constructor and `GaugeVec.With` label binding
- `opentelemetry-go/metric/sdk/metric/internal/aggregate/lastvalue.go` — per-label-set last-value pattern for synchronous gauge emulation
