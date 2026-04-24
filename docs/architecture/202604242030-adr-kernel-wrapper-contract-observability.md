# ADR: kernel/wrapper — Contract-level Observable Proxy

> Date: 2026-04-24 (revised after three review rounds)
> Status: Accepted
> Context PR: PR-A11 (`refactor/520-pr-a11-kernel-wrapper`)
> Tag: ADR-KERNEL-WRAPPER-CONTRACT-OBS-01
>
> **Revision note**: §3 auth.Mount policy ordering and §4 consumer
> observability are superseded in-place by §6 and §7 below (decisions 5-7
> added after review rounds 1+2 surfaced the silent-noop and panic-leak
> defects). Round 3 replaced the package-level-global §5 with
> constructor-injected tracer ownership at the runtime infrastructure
> layer (Router / eventrouter.Router) after integration test
> `examples/ssobff/walkthrough` panicked at first request because the
> test-embedded bootstrap never called `SetTracer` — the "fail loud on
> unset" design turned every new bootstrap entry point into a landmine
> instead of surfacing a real wiring bug. §4 is also updated: consumer
> WrapConsumer wiring is now in-scope for PR-A11 (symmetric with HTTP
> side) rather than deferred. §1/§2 remain unchanged.

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

Consumer wire-up (review round 3): `runtime/eventrouter.Router` gains a
`AddContractHandler(spec, handler, consumerGroup)` method — the
symmetric mirror of `auth.Mount(Route{Contract, ...})`. It wraps the
handler with `wrapper.WrapConsumer(r.tracer, spec, handler)` at
registration time using the Router-owned Tracer (injected via a new
`eventrouter.WithTracer` option during `bootstrap.phase6StartEventRouter`).
The legacy `AddHandler(topic, handler, cg)` stays as an untraced shim
for the remaining subscribers that PR-A11-M sweeps into contract
literals. One pilot cell (accesscore's `configreceive` subscription,
contract `event.config.changed.v1`) migrates to
`AddContractHandler` in this PR so the design is validated end-to-end
on both HTTP and event surfaces before `ContractSpec`'s shape is
frozen — previously deferring the consumer side to PR-A11-B would have
locked in the value-type fields based on the HTTP pilot alone.

### 5. Tracer Injection: Constructor-owned at runtime infrastructure layer

`kernel/wrapper` stays **stateless** — no package-level `var`, no
`SetTracer`. `HTTPHandler(tr, spec, next, opts...)` and
`WrapConsumer(tr, spec, fn)` accept the `Tracer` as a positional
parameter; a nil `tr` falls back to `NoopTracer{}` at the call site
(fail-open). The `Tracer` + `Span` interfaces, the `NoopTracer` value,
and the optional `TracerCarrier` helper all live in `kernel/wrapper`;
nothing in that package is mutable after program start.

Ownership of the live `Tracer` belongs to the runtime infrastructure
types that observe traffic:

- `runtime/http/router.Router` carries a `tracer wrapper.Tracer` field
  (seeded by the existing `router.WithTracer` option) and exposes it
  via `WrapperTracer()` which satisfies the new
  `wrapper.TracerCarrier` interface. Nested sub-muxes constructed by
  `Route` / `Group` / `With` inherit the parent's tracer. `auth.Mount`
  pulls the tracer via `wrapper.TracerFromCarrier(mux)` before calling
  `wrapper.HTTPHandler(...)` — a type assertion lookup so legacy mux
  types (`*http.ServeMux` in tests, stubs) that do not implement
  `TracerCarrier` transparently get `NoopTracer{}`.
- `runtime/eventrouter.Router` carries a `tracer wrapper.Tracer` field
  set via `eventrouter.WithTracer(...)`. `AddContractHandler` uses it
  to wrap subscribers with `WrapConsumer` at registration time.
- `runtime/bootstrap` threads the `Bootstrap.wrapperTracer` (captured
  by `WithTracer`) into both `router.WithTracer` (phase7) and
  `eventrouter.WithTracer` (phase6). No process-wide `SetTracer` is
  needed — the construction calls that receive the tracer are both
  compile-checked, so a new bootstrap entry point cannot accidentally
  drop the wiring and panic at first request.

Cells never see the tracer. `RegisterRoutes(mux cell.RouteMux)` and
`RegisterSubscriptions(r cell.EventRouter)` keep their existing
signatures; `auth.Mount(mux, Route{Contract, ...})` and
`r.AddContractHandler(spec, handler, "cellID")` are the contract-first
registration verbs from the Cell author's perspective.

Rationale — why this pattern is correct despite §E below originally
rejecting a similar "explicit option + noop default" approach:

1. **GoCell-native ownership**: `kernel/wrapper` is the value/rules layer
   (aligned with `kernel/metadata`, `kernel/outbox` — interfaces and
   value types, no live resources). Live `Tracer` instances are a
   runtime resource, so they belong in the runtime infrastructure that
   runs the request / event loop (`Router`, `eventrouter.Router`), not
   on a kernel package-level variable.
2. **Compile-time enforcement**: with tracer as a constructor parameter
   on Router/eventrouter, `bootstrap` cannot forget to thread it —
   missing an argument is a compile error. The rejected-E failure mode
   ("no caller was passing WithTracer") was an artifact of placing
   `WithTracer` as a functional option on HTTPHandler, where Cells —
   not bootstrap — would have had to pass it through. Under §5,
   bootstrap is the only caller of `router.WithTracer` /
   `eventrouter.WithTracer`, and both APIs are pre-existing.
3. **Test ergonomics**: spy tracers inject through `router.WithTracer`
   (integration harness) or directly into `HTTPHandler(tr, …)` /
   `WrapConsumer(tr, …)` (unit tests). No `SetTracer` / `ResetTracer`
   setup/teardown dance, no race conditions on a shared package
   variable, no `t.Parallel()` landmines. `handler_test.go` /
   `consumer_test.go` construct a `spyTracer` per test as a local
   value.
4. **No landmine for future bootstraps**: every new composition root
   (examples/ssobff's `buildWalkthroughServer`, future embedded modes,
   sidecar tools) gets a compiled-in noop via
   `wrapper.TracerFromCarrier` fallback. Adding `-race` tests
   that run integration paths never surfaced a tracer-wiring bug
   because the runtime infrastructure is the single injection site and
   `Router` construction is the only entry.

`HTTPHandler` / `WrapConsumer` / `HandlerOption` / `WithFilter` —
filter suppression is still an orthogonal concern, retained as the
only `HandlerOption` (probe paths skip span creation entirely).

ref: `log/slog` Logger type — constructor `slog.New(handler)` is the
preferred shape; `slog.Default()` is convenience for untyped callers,
not the canonical API. We adopt the constructor shape uniformly
because the runtime infrastructure layer is always typed.

ref: `open-telemetry/opentelemetry-go/sdk/trace.TracerProvider` —
construction-time configuration via options; provider instance is
passed to instrumentations, not read from a package-level getter at
span-creation time.

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

### E. Explicit `HandlerOption` Option (`WithTracer`) + `NoopTracer{}` default
Rejected in round 1 and **still** rejected. The round-1 defect was not
"Option pattern bad in absolute terms" but "Option placed on the wrong
layer": making `HTTPHandler` accept `WithTracer(...)` pushed the
tracer wiring responsibility onto Cell authors, who had no reason to
thread it from `Cell.RegisterRoutes` through `auth.Mount`. Zero cells
passed it, so every contract span silently became noop. §5 puts the
tracer on the runtime infrastructure layer (Router / eventrouter.Router
constructors) where bootstrap is the sole caller — different pattern,
different failure mode, which is why it works where the round-1
HandlerOption did not.

### E-prime. Package-level global `SetTracer` (the round-2 answer, reverted in round 3)
Rejected in round 3. The round-2 package-level global worked in unit
tests and on the round-2 integration harness, but
`examples/ssobff/walkthrough_test` built its HTTP server directly
(`buildWalkthroughServer`) without calling `wrapper.SetTracer`. The
`panicIfNotSetTracer` sentinel that was supposed to surface mis-wired
binaries on day 0 instead fired on the first `POST /login` inside
Recovery middleware, 500ing the test. The underlying defect: any new
bootstrap entry point becomes a landmine; "fail loud on unset" is
indistinguishable from "fail loud on missing wiring" because the
panic occurs deep in the HTTP stack, not at startup. §5's
constructor-injected ownership gets the wiring guarantee via compile
errors on `router.New` / `eventrouter.New` call sites (only
`bootstrap` constructs them) without the landmine.

### F. Context-propagated tracer (`ctxkeys.WithTracer`)
Rejected as a middle ground. The middleware layer setting
`ctx.Value(tracerKey, t)` is cleaner than a `HandlerOption` but adds a
second potential failure path — if a Cell somehow serves a request
without passing through the runtime HTTP middleware chain (test
harnesses, sidecar sockets, future embedded modes) the tracer is
missing again. Compared against §5's construction-time injection on
Router: ctx propagation would have added a per-request lookup in the
hot path, plus a silent-noop failure mode for any code path that
bypasses middleware. The §5 approach pays zero per-request cost
(tracer is a field on the Router pulled once at registration) and
catches the wiring gap at the `wrapper.TracerFromCarrier` type
assertion — if a mux without `TracerCarrier` is passed, the fallback
is visible `NoopTracer{}` that tests can assert against.

## Follow-ups (registered in docs/backlog.md)

- `PR-A11-M CELL-ROUTES-MOUNT-MIGRATION-01` — migrate remaining ~33
  HTTP routes and ~5 consumer subscriptions
  (cells/accesscore/configcore/auditcore + examples) from legacy
  `auth.Declare` / `r.AddHandler(topic, ...)` to `auth.Mount(…Route{…})` /
  `r.AddContractHandler(spec, …)`; delete `auth.Declare` + `RouteDecl`
  and `EventRouter.AddHandler(topic, …)` at the end.
- `PR-A11-V FMT-17-SPEC-CONTRACT-SYNC-01` — governance rule that
  grep-scans `wrapper.ContractSpec{}` literals in cells/examples and
  cross-references against `contracts/**.yaml`; drift = validation
  error at `gocell validate --strict`. Covers both HTTP (method/path)
  and event (topic/kind) dimensions after §5 wired both sides.
- `PR-A11-S LOGGING-SLOG-CONTRACT-ID-01` — teach
  `runtime/observability/logging.contextHandler` to read
  `kernel/ctxkeys.ContractID` and emit `contract_id` on every slog
  record (today it only emits trace_id/span_id/cell_id).
- `PR-A11-R1 WRAPPER-NO-PACKAGE-STATE-LINT-01` — new FMT governance
  rule that scans `kernel/wrapper/*.go` for package-level `var`
  declarations with interface or pointer types. §5's
  constructor-injection discipline must stay — any regression that
  re-introduces a `var tracer Tracer` (or similar live-resource
  singleton) in kernel/wrapper should fail `gocell validate --strict`
  with a hint to thread the dependency through the construction path
  instead. Constants and immutable zero-value sentinels (NoopTracer{})
  are allowed; the rule flags only mutable global state.

Note: the prior `PR-A11-B OUTBOX-CONSUMER-WRAPPER-01` follow-up is now
done in this PR (see §4 round-3 update). No open consumer-side
follow-up remains at the wiring level — only the mechanical
migration, folded into PR-A11-M.

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
