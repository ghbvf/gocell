# ADR: kernel/wrapper — Contract-level Observable Proxy

> Date: 2026-04-24 (revised after two review rounds)
> Status: Accepted
> Context PR: PR-A11 (`refactor/520-pr-a11-kernel-wrapper`)
> Tag: ADR-KERNEL-WRAPPER-CONTRACT-OBS-01
>
> **Revision note**: §3 auth.Mount policy ordering and §4 consumer
> observability are superseded in-place by §6 and §7 below (decisions 5-7
> added after review rounds 1+2 surfaced the silent-noop and panic-leak
> defects). §1/§2 remain unchanged.

## Context

PR-A9 (`CONTRACT-META-01`, PR #239) elevated HTTP contract transport
metadata (method, path, path/query params, success status, responses) to
first-class values on `pkg/contracts.HTTPTransport` and extended governance
to cross-check path templates vs. declared path params (FMT-13). The
follow-on gap: **at runtime no observability channel carries the owning
contract id.** Trace spans derive their name from chi's `RouteContext`
alone; Prometheus labels bucket by method/route/status; structured logs
include trace_id/span_id but not `contract_id`. Operators looking at
Jaeger/Tempo cannot aggregate by contract and SREs auditing outlier
latency must guess which contract a span belongs to.

## Decision

Introduce a new kernel-layer module `kernel/wrapper` that binds
contracts to runtime observability primitives. The design has four parts:

### 1. Kernel owns Tracer/Span interfaces

`kernel/wrapper.Tracer` and `kernel/wrapper.Span` replace the definitions
previously living in `runtime/observability/tracing`. `runtime/observability/
tracing.Tracer` / `.Span` / `.Attr` are now **type aliases** of the kernel
interfaces so downstream callers keep compiling. CLAUDE.md's LAYER-01
rule (kernel ⇏ runtime/adapters/cells, third-party import allowlist: stdlib
+ `pkg/*` + `gopkg.in/yaml.v3`) stays clean — OTel adapter lives in
`adapters/otel` as the concrete kernel.Tracer implementation.

Rationale: `kernel/wrapper.HTTPHandler` and `kernel/wrapper.WrapConsumer`
both need to start spans. Duplicating a second Tracer interface in
`kernel/wrapper` would force the runtime middleware to bridge, adding
friction with zero payoff. Moving the already-clean Go interface down to
kernel is a one-commit refactor that preserves behaviour and unifies
the abstraction.

### 2. ContractSpec is a value type, not a registry lookup

`kernel/wrapper.ContractSpec{ID, Kind, Transport, Method, Path, Topic}`
is a plain value type. Cells construct literals **inline** next to each
handler / subscription — one `var loginSpec = wrapper.ContractSpec{...}`
per route. No runtime catalogue, no `//go:embed contracts/**` parse
cost, no contract registry dependency injection.

The duplication with YAML is intentional: it stays caught by a future
`FMT-17 SPEC-CONTRACT-SYNC` governance rule that cross-references Go
literal usage sites against `contracts/**.yaml` at `gocell validate
--strict` time. Until FMT-17 lands, hand-maintained literals are the
minimum-viable path — no worse than today's `auth.RouteDecl{Method,Path}`
duplication.

Rationale: introducing a runtime catalogue would pull `//go:embed` + a
YAML parse into every binary, cascade into the assembly/bootstrap wire-
up, and require changes to every example's main.go. The FMT-17 gate
gives equivalent drift protection statically, at CI time, without any
runtime cost.

Reference: `k8s.io/apimachinery` — shared lightweight types exposed to
higher layers without pulling in a parser.

### 3. `auth.Mount` + `auth.Route` replace `auth.Declare` + `auth.RouteDecl`

`runtime/auth.Mount(mux, Route{Contract, Handler, Policy, ...})` is the
new route-registration entry point. `Route.Contract` is optional: when
set, `wrapper.HTTPHandler` wraps the Handler so every request emits a
span tagged with `gocell.contract.id` / `kind` / `transport` plus the
standard `http.method` / `http.route` / `http.status_code`. Nil Contract
preserves the legacy path (no contract attribute).

`auth.Declare` + `auth.RouteDecl` become a thin legacy shim forwarding
to `Mount` with `Contract` left zero. All existing call sites keep
compiling; a follow-up migration (see PR-A11-M backlog entry) sweeps
the remaining ~33 HTTP routes + examples onto `Mount`.

Rationale: A hard cut would force every cell_routes.go in the repo to
change in the same commit, inflating the PR review surface past
architectural intent. Shim + migration batch keeps each commit small
and verifiable.

### 4. `wrapper.WrapConsumer` for outbox event consumers

`wrapper.WrapConsumer(spec, fn)` returns a contract-tagged
`outbox.EntryHandler`. Span name `"CONSUME {topic}"`, attrs
`gocell.contract.id` + `messaging.system` + `messaging.destination`.
Ack / Requeue / Reject dispositions flow through unchanged; the
wrapper only records status/error info on the span.

Panic safety (review round 2): the closure installs a `defer` that
recovers any handler panic, marks the span `StatusError` + `RecordError`,
ends the span, then re-panics so the outer `Router.runSubscribe`
recover still sees it. HTTPHandler shares the same `recoverAndFinish`
primitive in `kernel/wrapper/lifecycle.go`, eliminating the pre-review
asymmetry where HTTP recovered panics but consumers leaked spans.

Wire-up to `kernel/outbox.ConsumerBase` / `runtime/eventrouter` is
deferred to a follow-up PR-A11-B (see backlog) so the event-consumer
migration does not entangle with the current HTTP-side refactor.

### 5. Tracer Injection: Package-Level Global (supersedes the Option pattern)

`kernel/wrapper` owns a package-level `var tracer Tracer`. The zero value
is `panicIfNotSetTracer{}` — its `Start` panics with a message naming
`runtime.bootstrap` as the required caller, so any first request on a
mis-wired binary fails loudly on day 0 rather than silently producing
noop spans.

`wrapper.SetTracer(t)` is the single write path and is called exactly
once by `runtime/bootstrap.phase1LoadConfig`. When no tracer was
supplied via `Bootstrap.WithTracer`, phase1 falls back to
`wrapper.NoopTracer{}` with a `slog.Warn` so the wiring gap is visible
in operator logs without breaking startup.

`HTTPHandler` and `WrapConsumer` now take no tracer parameter and no
`WithTracer` option. `HandlerOption` retains only `WithFilter` for
probe suppression (`wrapper.DefaultProbeFilter` matches
`/healthz` / `/readyz` / `/livez`).

Rationale: matches `log/slog.Default()` / `otel.GetTracerProvider()` /
Kratos `otel.Tracer(name)` / Watermill-otel `otel.Tracer(...)` — the
industry-standard "process-wide singleton, injected once by the
application entrypoint" pattern. Kernel LAYER-01 stays clean because
`SetTracer` is a kernel-defined API and runtime → kernel is the
allowed direction.

### 6. Policy Within Contract Span (supersedes §3's policy ordering)

`auth.Mount` now wraps `RequirePolicy` **inside** `wrapper.HTTPHandler`.
Policy denials (403/401) therefore emit a complete contract span
tagged with `gocell.contract.id`, so operators aggregating error
rates by contract see authorization failures — a dimension invisible
under the earlier "policy outside wrapper" ordering.

Cost: every pre-auth unauthorized request now produces a span.
This is cheap in absolute terms (one span per request, same as any
200 OK request) and orthogonal to the layering model — if unauth
traffic ever becomes an observability cost issue, apply a sampler
keyed on `http.status_code` rather than rearranging the middleware
chain. Removed: the "Do NOT swap this order" comment that the initial
PR introduced in `runtime/auth/route.go`.

Contract.Path drift is now statically invariant-enforced in
`validateContractShape`: non-empty `Contract.Path` must have
`path.Clean(Route.Path)` as suffix, and `Contract.Kind == "http"`
requires `Contract.Path != ""`. Prevents the silent drift that the
earlier ADR version explicitly deferred to FMT-17 (which remains a
follow-up PR-A11-V for the contract ⇄ YAML cross-check).

### 7. Outer request span skipped when contract span covers

`runtime/http/middleware/tracing.go`'s `Tracing` middleware now
short-circuits when `ctxkeys.ContractIDFrom(ctx)` returns present —
i.e. the inner contract span has already been opened upstream. This
eliminates the double-span emission that the initial ADR version
listed as a migration-window cost ("Span coexistence note"). Result:
one request → one span, tagged with `gocell.contract.id`, as soon as
a Cell migrates its route to `auth.Mount` + `Route.Contract`.

Routes without a `Contract` continue through `middleware.Tracing`
unchanged (chi-route-pattern span), so PR-A11-M's route migration
can proceed gradually without double-counting any requests.

## Consequences

### Positive
- Trace spans + slog fields + metrics carry `contract_id` uniformly
  once cells migrate to `Mount`. Jaeger filter-by-contract becomes a
  first-class operation.
- kernel/wrapper keeps LAYER-01 invariants; no new third-party
  imports leak into kernel.
- The zero-dependency `ContractSpec` value type keeps the refactor
  surgical — no catalogue bootstrap, no codegen, no runtime parse.
- `auth.Mount` is a strict superset of `auth.Declare`; migrations are
  mechanical diff-expansions, reviewable per-cell.

### Negative
- Until FMT-17 (PR-A11-V) ships, `ContractSpec` literal content still
  can drift from `contracts/**.yaml`, but `Contract.Path` vs
  `Route.Path` suffix invariance is now a runtime-validated
  precondition, closing the most common mistake.
- `auth.Declare` stays in-tree during the migration window. Two APIs
  co-existing is noise; future cleanup PR-A11-M + PR-A11-B close this.
- `kernel/outbox.ConsumerBase` + `runtime/eventrouter.Router.AddHandler`
  keep today's topic-string API pending PR-A11-B. Event consumers
  therefore still have no `contract_id` attribute today.
- Unauthorized traffic now produces spans (cost of §6's policy-inside
  model). Apply `http.status_code` samplers downstream if volume
  becomes a backend cost issue.

### Neutral
- `runtime/observability/tracing` Tracer/Span interface move is source-
  compatible via type aliases; callers see no breakage.
- Performance: the wrapper adds one `ctxkeys.WithContractID` call, one
  `tracer.Start` (which is the OTel call that was always there), five
  `SetAttributes` allocations per request. Negligible in the HTTP hot
  path (<1 µs vs. stdlib ServeHTTP cost).

## Rejected Alternatives

### A. Direct OTel import in kernel
Rejected. Violates LAYER-01 (third-party dependency in kernel/). A
thin `Tracer` interface + OTel adapter in `adapters/otel` costs ~15
lines and preserves portability.

### B. Runtime contract catalogue (`//go:embed contracts/**`)
Rejected for this PR. Adds startup cost, wire-up complexity (every
main.go + example needs embed + registry), and scope creep that would
double the PR surface. Governance-level cross-check (FMT-17) gives the
same drift protection at zero runtime cost. Re-evaluate if cells grow
past ~100 routes or cross-contract aggregation needs escape the CI
static layer.

### C. Codegen contract catalogue (`gocell generate contract-catalog`)
Rejected for this PR — same reason as B, plus would require a new
`go generate` step in the build. Potential future evolution when the
`generated/` directory has more consumers.

### D. `RouteDecl.ContractID` (keep old shape, add optional field)
Rejected. Adds ambiguity: "when should a caller populate ContractID?"
and cannot enforce the Method/Kind invariants that `wrapper.ContractSpec`
carries as a value type. The `Route` + `ContractSpec` split makes the
observability contract a first-class field, not a retrofitted string.

### E. Explicit Option (`WithTracer`) + `NoopTracer{}` default
Rejected after review round 1 exposed that no caller was passing
`WithTracer(...)` — `auth.Mount` has no tracer parameter and no ctx
extraction, so every contract span silently became noop. Unlike
otelhttp / Kratos / Watermill which fall back to the process-wide
`otel.GetTracerProvider()`, kernel/wrapper's noop default had no
second chance: zero spans on the dashboard looked identical to "we
forgot to wire it" and "tracing is globally disabled". The §5
package-level global + startup panic on unset surfaces wiring gaps on
day 0 instead.

### F. Context-propagated tracer (`ctxkeys.WithTracer`)
Rejected as a middle ground. The middleware layer setting
`ctx.Value(tracerKey, t)` is cleaner than an Option but adds a second
potential failure path — if a Cell somehow serves a request without
passing through the runtime HTTP middleware chain (e.g. test
harnesses, sidecar sockets, future embedded modes) the tracer is
missing again. Package-level `SetTracer` bound once at startup has no
such gap: every call site in the process shares the same instance.
Compared against §5: ctx propagation would have required no API
change to Mount, but kept the "multiple injection sites + partial
wiring" failure mode alive.

## Follow-ups (registered in docs/backlog.md)

- `PR-A11-M CELL-ROUTES-MOUNT-MIGRATION-01` — migrate remaining ~33
  HTTP routes across cells/accesscore/configcore/auditcore + examples
  to `auth.Mount`; delete `auth.Declare` + `RouteDecl` at the end.
- `PR-A11-B OUTBOX-CONSUMER-WRAPPER-01` — extend
  `kernel/outbox.ConsumerBase` + `runtime/eventrouter.Router.AddHandler`
  to accept `wrapper.ContractSpec`; migrate all existing subscribers.
- `PR-A11-V FMT-17-SPEC-CONTRACT-SYNC-01` — governance rule that
  grep-scans `wrapper.ContractSpec{}` literals in cells/examples and
  cross-references against `contracts/**.yaml`; drift = validation
  error at `gocell validate --strict`.
- `PR-A11-S LOGGING-SLOG-CONTRACT-ID-01` — teach
  `runtime/observability/logging.contextHandler` to read
  `kernel/ctxkeys.ContractID` and emit `contract_id` on every slog
  record (today it only emits trace_id/span_id/cell_id).
- `PR-A11-R1 WRAPPER-TRACER-LINT-GUARD-01` — new FMT governance rule
  that scans `kernel/wrapper/*.go` public function signatures and
  rejects any `Tracer` parameter outside `SetTracer`; prevents future
  regressions that would re-introduce the bypass path §5 eliminated.

## References

- `ref: go-kratos/kratos middleware/tracing/tracing.go@main` —
  middleware decorator + Options pattern
- `ref: go-kratos/kratos middleware/tracing/span.go@main` —
  http.method / route / status_code attribute set
- `ref: open-telemetry/opentelemetry-go-contrib
  instrumentation/net/http/otelhttp/config.go@main` —
  `SpanNameFormatter` + `Filter` extensibility points
- `ref: riandyrn/otelchi middleware.go@master` — chi two-phase span
  rename post-ServeHTTP
- `ref: zeromicro/go-zero rest/handler/tracehandler.go@master` —
  explicit path parameter at registration time
- `ref: uber-go/fx app.go@master` — construction-time injection over
  global state (rejected for cross-cutting tracer; accepted pattern
  for Cell services)
- `ref: log/slog slog.go@go1.22` — `Default()` / `SetDefault()`
  process-wide logger singleton — §5 uses the same shape for Tracer
- `ref: open-telemetry/opentelemetry-go otel.go@main` —
  `GetTracerProvider()` / `SetTracerProvider()` global provider
  pattern adopted by otelhttp / Kratos / Watermill
- Rejected: `ref: kubernetes/apimachinery pkg/util/runtime/runtime.go@
  master` global `PanicHandlers` singleton pattern (untestable + kernel
  LAYER-01 violation)

## Supersedes / Related

- LATER-K1 `KERNEL/WRAPPER` (registered in docs/plans/202604232330-025)
- PR-A9 `CONTRACT-META-01` (dependency)
- PR-A10 `OUTPUT-JSON-SARIF` (downstream consumer of contract ids in diagnostics)
- PR-A36 `HTTP-METRICS-LABEL-REALIGN` (separate PR — different concern)
