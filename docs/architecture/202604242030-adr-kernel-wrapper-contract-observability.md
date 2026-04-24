# ADR: kernel/wrapper — Contract-level Observable Proxy

> Date: 2026-04-24
> Status: Accepted
> Context PR: PR-A11 (`refactor/520-pr-a11-kernel-wrapper`)
> Tag: ADR-KERNEL-WRAPPER-CONTRACT-OBS-01

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

`wrapper.WrapConsumer(spec, fn, opts...)` returns a contract-tagged
`outbox.EntryHandler`. Span name `"CONSUME {topic}"`, attrs
`gocell.contract.id` + `messaging.system` + `messaging.destination`.
Ack / Requeue / Reject dispositions flow through unchanged; the
wrapper only records status/error info on the span.

Wire-up to `kernel/outbox.ConsumerBase` / `runtime/eventrouter` is
deferred to a follow-up PR-A11-B (see backlog) so the event-consumer
migration does not entangle with the current HTTP-side refactor.

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
- Until FMT-17 ships, `ContractSpec` literals can drift from YAML
  (but today's `Method`/`Path` fields on `RouteDecl` have the exact
  same exposure — no regression, just a deferred hardening step).
- `auth.Declare` stays in-tree during the migration window. Two APIs
  co-existing is noise; future cleanup PR-A11-M + PR-A11-B close this.
- `kernel/outbox.ConsumerBase` + `runtime/eventrouter.Router.AddHandler`
  keep today's topic-string API pending PR-A11-B. Event consumers
  therefore still have no `contract_id` attribute today.

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
  global state
- Rejected: `ref: kubernetes/apimachinery pkg/util/runtime/runtime.go@
  master` global `PanicHandlers` singleton pattern (untestable + kernel
  LAYER-01 violation)

## Supersedes / Related

- LATER-K1 `KERNEL/WRAPPER` (registered in docs/plans/202604232330-025)
- PR-A9 `CONTRACT-META-01` (dependency)
- PR-A10 `OUTPUT-JSON-SARIF` (downstream consumer of contract ids in diagnostics)
- PR-A36 `HTTP-METRICS-LABEL-REALIGN` (separate PR — different concern)
