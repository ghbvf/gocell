# Trace Propagation Plan

## Context

- Backlog item: `TRACE-PROP-01` in `docs/backlog.md`
- Goal: preserve inbound distributed trace continuity for HTTP requests by extracting upstream trace headers before starting the server span
- Scope: `src/runtime/http/middleware/`, `src/runtime/observability/tracing/`, `src/runtime/http/router/` tests, and `src/go.mod` if a B3 propagator dependency is needed
- Non-goal: outbound client injection, event metadata bridge, or tracing metrics expansion

## Team Exploration

### OpenTelemetry `otelhttp`

- Pattern: `traceparent` and related headers are extracted before the server span starts
- Mechanism: use a `TextMapPropagator` over `http.Header`, then let `Tracer.Start` consume the extracted parent context
- Implication for GoCell: extraction belongs at the HTTP ingress boundary, not inside business handlers

### Kratos

- Pattern: server middleware passes a carrier into tracing startup and extracts before span creation
- Mechanism: transport layer owns header carrier creation; tracing stays transport-agnostic after context is prepared
- Implication for GoCell: `Tracing` middleware should prepare parent context first, then keep the rest of the middleware contract unchanged

### go-zero

- Pattern: inbound HTTP middleware extracts trace headers before `tracer.Start`
- Mechanism: W3C Trace Context is primary; missing or invalid upstream headers safely degrade to a new root span
- Implication for GoCell: preserve current fail-safe behavior while making valid upstream headers continuous across services

## Current Gap And Root Cause

### Current behavior

- `runtime/http/middleware/Tracing` calls `tracer.Start(r.Context(), ...)` directly
- No inbound `traceparent` or B3 extraction occurs before that call
- Result: every inbound request starts a new root trace even when upstream services propagated tracing headers

### Root cause

- Trace propagation is missing at the HTTP ingress boundary
- `simpleTracer` can already reuse `ctxkeys.TraceID` when present, and the OTel adapter can already inherit a remote parent from context, but the request context is never populated from headers
- This is a middleware ordering problem, not a span lifecycle problem

## Implementation Decision

### Chosen design

Add an HTTP extraction helper in `runtime/http/middleware` and call it at the top of `middleware.Tracing` before `tracer.Start`.

### Why this shape

- Fixes the problem at the ingress boundary where it originates
- Keeps the `router.WithTracer` and `bootstrap.WithTracer` APIs unchanged
- Works for both the lightweight in-repo tracer and the OTel adapter
- Avoids pushing HTTP header awareness into business code

### Concrete decisions

1. Add an internal HTTP extraction helper in `src/runtime/http/middleware/`
2. Use W3C Trace Context as primary and B3 only as a fallback
3. When extraction yields a valid remote span context, mirror the extracted trace ID into `ctxkeys` so the simple tracer preserves continuity too
4. Keep invalid or absent headers as a safe no-op so the current root-span fallback remains intact
5. Do not change response or API contracts

## TDD Plan

### Step 1: Reproduction tests first

Add failing tests that prove the current gap:

- `runtime/http/middleware/tracing_test.go`
- `runtime/http/router/router_test.go`

### New failing test cases

1. `TestTracing_UsesTraceparentTraceID`
2. `TestTracing_UsesB3SingleHeaderTraceID`
3. `TestTracing_UsesB3MultiHeaderTraceID`
4. `TestTracing_InvalidTraceHeadersStartNewRoot`
5. `TestWithTracer_ExtractsUpstreamTraceparent`

### Assertions

- Valid upstream headers keep the same `trace_id`
- A new server span still gets a new `span_id`
- Invalid headers do not panic and do not poison context
- Existing no-tracer behavior remains unchanged

## File-Level Task List

- [ ] Add tracing extraction helper with table-driven tests in `src/runtime/http/middleware/`
- [ ] Update `src/runtime/http/middleware/tracing.go` to extract before `tracer.Start`
- [ ] Extend middleware tests for W3C and B3 propagation
- [ ] Extend router integration tests to prove end-to-end ingress extraction
- [ ] Update dependencies only if `go.opentelemetry.io/contrib/propagators/b3` is not already available
- [ ] Run focused build and tests
- [ ] Create PR against `develop`
- [ ] Launch six-role review and collect findings
- [ ] Fix all confirmed C1 and C2 findings

## Verification Matrix

Run at minimum:

```bash
cd src
go test ./runtime/http/middleware ./runtime/http/router ./adapters/otel
go build ./...
```

If the implementation changes OTel adapter behavior or shared tracing utilities, expand to:

```bash
cd src
go test ./adapters/otel/...
```

## Expected PR Outcome

- Backlog item `TRACE-PROP-01` becomes actionable on `fix/219-trace-propagation`
- Inbound HTTP requests honor upstream `traceparent` and B3 headers
- Trace continuity works without changing public bootstrap or router APIs