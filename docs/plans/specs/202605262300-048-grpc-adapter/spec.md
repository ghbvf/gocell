# Feature Specification: gRPC Transport Adapter

**Feature Branch**: `515-grpc-adapter`
**Created**: 2026-05-26
**Status**: Draft
**Input**: User description: "Introduce a gRPC transport adapter alongside HTTP and AMQP so cells can declare gRPC contracts in contract.yaml and have codegen derive server stubs and client invokers, with full parity to existing capabilities (typed envelope, errcode redaction, panic funnel, cell label, readyz, principal propagation, observability, archtest). Sliced delivery: each PR strictly ≤ 2000 lines (code + tests + docs)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Cell author declares an RPC contract once and gets a working endpoint (Priority: P1)

A cell author writes one `contract.yaml` entry describing an RPC operation (service name, method name, request/response shape, error response set, optional streaming pattern) and runs `gocell generate`. Framework + codegen produce a server-side handler interface (which the cell implements) and a client invoker (which other cells or external callers consume). The author never writes transport plumbing.

**Why this priority**: Without this, every RPC endpoint is hand-wired transport code — defeating the cell model. This is the irreducible MVP slice. Until this works end-to-end for one unary operation, no other story can deliver value.

**Independent Test**: Add a `kind: grpc` operation to an example cell's `contract.yaml`, regenerate, implement the handler interface, run the example assembly, call the operation from a generated client → response round-trips with the correct typed envelope. No transport code is hand-written.

**Acceptance Scenarios**:

1. **Given** a cell with one unary RPC operation declared in contract.yaml, **When** `gocell generate` runs, **Then** the framework emits a server handler interface + a typed client invoker + a registration funnel — the cell author's only required code is the business logic implementing the interface.
2. **Given** the cell's RPC handler is implemented, **When** the assembly boots and a client calls the operation, **Then** the response is returned with the same observability shape (metric / log / trace) as an equivalent HTTP operation for that cell.
3. **Given** the RPC handler returns a domain error, **When** the client receives the response, **Then** the error carries the same redaction layers (public message, public details, internal-only context elided) as the equivalent HTTP response.

---

### User Story 2 — Operator gets parity observability + health for the new transport (Priority: P1)

An operator monitoring an assembly that includes both HTTP and RPC operations sees one unified observability surface — metrics labelled by the same `cell` dimension, the same `request_id` / `correlation_id` / `trace_id` propagation, the same `readyz` verbose payload structure — without per-transport dashboards or per-transport alert rules.

**Why this priority**: P1 because shipping a new transport with split observability is operationally untenable in this codebase. The framework's identity is "cell-attributed, redaction-by-default, single-source observability". A divergent transport breaks that identity on day one.

**Independent Test**: Boot an assembly with at least one RPC operation per cell. Verify `/metrics` exposes the new transport's series under the same `cell` label set, `/readyz?verbose` includes a typed `grpc_ready` probe, and a request that crosses HTTP → cell-internal → RPC retains its `correlation_id` end-to-end in slog.

**Acceptance Scenarios**:

1. **Given** an RPC operation is in-flight, **When** the operator queries `/metrics`, **Then** there is a per-cell counter and latency histogram with the same `cell` label semantics as HTTP, and no metric uses `cell="_runtime"` for a business-owned method.
2. **Given** the RPC server is degraded (port unbound, listener stalled), **When** `/readyz` is queried, **Then** the `grpc_ready` probe reports a typed status that surfaces the failure cause through the same four-channel redaction model as HTTP probes.
3. **Given** an inbound RPC call enters the system, **When** it eventually emits an outbox event, **Then** the event envelope carries the same `correlation_id` / `principal envelope` (where applicable) as if the call had entered via HTTP.

---

### User Story 3 — Service-to-service calls between cells use a typed client (Priority: P2)

When cell A calls cell B (in the same assembly or a sibling assembly), the call goes through a generated typed client invoker — not a hand-rolled HTTP client, not a raw RPC stub. The client surfaces the same `errcode.Error` shape on failure; the call participates in the caller's trace span; deadlines propagate.

**Why this priority**: P2 because the MVP (P1) gets the server side working with a generated client, which lets other cells consume it. The deeper integration — uniform retry, deadline policy, observability on the client side — is value but not blocking the first usable transport.

**Independent Test**: Generate two example cells where A calls B over RPC. Verify A's logs show the call as a child span of an HTTP request that hit A, B's logs show the same `trace_id`, and a synthetic error in B surfaces in A as a typed `*errcode.Error` carrying the public message + details but not the internal context.

**Acceptance Scenarios**:

1. **Given** cell A has a generated client for cell B's RPC operation, **When** A invokes the operation inside its own handler, **Then** the call inherits A's context (deadline, trace, correlation_id, principal envelope where applicable).
2. **Given** B returns an `*errcode.Error` with internal context, **When** A receives the response, **Then** A sees a public message + details, but the internal context is not present on the wire.

---

### User Story 4 — Streaming patterns support watch / fanout / upload scenarios (Priority: P3)

The framework supports server-streaming (e.g., watch a resource), client-streaming (e.g., chunked upload), and bidirectional streaming (e.g., interactive command channel). Stream lifecycle integrates with cell shutdown (drains in-flight streams gracefully) and observability (one span per stream, with stream-level metric for bytes/messages).

**Why this priority**: P3 because most operations are unary; streaming is a multiplier for specific scenarios (watch, upload, interactive). It is genuinely needed but can ship after the unary path proves out.

**Independent Test**: An example cell exposes a server-stream `Watch(filter) → stream Event` operation. Subscribe from a client; verify events flow until the client closes or the cell shuts down (clean half-close on shutdown).

**Acceptance Scenarios**:

1. **Given** a server-stream RPC, **When** a client subscribes, **Then** the stream stays open until either side closes; cell shutdown closes pending streams within the drain window and emits a half-close with a typed reason.
2. **Given** a client-stream RPC, **When** the client uploads chunks, **Then** the handler receives them in order, can fail mid-stream with a typed `*errcode.Error`, and the partial state is rolled back consistently with the cell's `consistencyLevel` (L1/L2 semantics preserved).

---

### Edge Cases

- **Port collision / listener bind failure** at boot: surfaces via `readyz` (not `livez`); fails fast with structured slog; assembly does not silently degrade to HTTP-only.
- **Deadline exhaustion** mid-handler: handler observes context cancellation, returns a typed `*errcode.Error{Kind=DeadlineExceeded}` that round-trips to client; no panic, no leaked goroutine.
- **Panic in handler**: caught by Recovery interceptor, wrapped via the existing `panicregister.Approved` funnel, surfaces to client as a typed internal error with redacted internal context.
- **Client cancellation mid-stream**: handler sees `ctx.Done()`, releases resources, no leaked stream; observed as a metric label distinct from successful close.
- **Mixed-version deployment**: clients holding an older proto descriptor talk to a server with newer methods — backward compatibility follows the same `Pre-v1.0 direct evolution` rule as HTTP (no v2 indirection during pre-GA).
- **Cell label resolution failure**: an RPC call that cannot resolve its owning cell falls back to the `_runtime` sentinel exactly like HTTP — never silently labels a business call as runtime.
- **Internal-only RPC accidentally exposed publicly**: contracts declared internal MUST NOT mount on the public RPC listener; verified by archtest.
- **Codegen output drift**: regenerating after hand-editing the generated stubs MUST detect the divergence (golden + regenerate-and-diff).
- **Proto registry collisions**: two cells defining the same proto package + service is a generate-time fail-fast.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Cells MUST be able to declare RPC operations via `contract.yaml` with `kind: grpc` and have codegen derive the server handler interface and client invoker.
- **FR-002**: The four RPC interaction patterns — unary, server-stream, client-stream, bidirectional — MUST all be supported by the framework (delivery may slice unary first and streaming in a later PR within the same feature).
- **FR-003**: Generated RPC code MUST follow the existing typed envelope discipline: server handlers return a typed success/error response sum type; framework converts to wire format.
- **FR-004**: `*errcode.Error` MUST round-trip through the RPC wire with the same Message / Details / Internal / Diagnostics four-channel separation as HTTP. Internal context MUST NEVER appear on the wire.
- **FR-005**: RPC handlers MUST run inside an equivalent middleware chain to HTTP (Recovery, Metrics, Tracing, AccessLog, Auth, RateLimit, CircuitBreaker as applicable; CORS / BodyLimit / SecurityHeaders may be N/A by design — that decision MUST be explicit, not implicit, and enforced by archtest).
- **FR-006**: Metrics emitted by RPC handlers MUST use the same `cell` label semantics as HTTP, derived through a listener-level cell-attribution mechanism; business operations MUST NOT label as `_runtime`.
- **FR-007**: RPC readiness MUST be surfaced through a typed `kernel/healthz.ReadyProbeName` (e.g., `grpc_ready`), participating in the same `/readyz` verbose four-channel redaction model.
- **FR-008**: `request_id`, `correlation_id`, `trace_id`, and Principal envelope MUST propagate end-to-end through RPC calls — both inbound (from client to handler) and outbound (handler invoking another cell's RPC client).
- **FR-009**: TLS termination MUST be supported; mTLS MUST be supported for service-to-service deployments. The configuration surface follows the same convention as HTTP (no plaintext defaults in non-dev profiles).
- **FR-010**: Generated RPC code MUST NOT be hand-edited; a regenerate-and-diff golden mechanism MUST detect divergence (Hard-grade enforcement).
- **FR-011**: Cell-to-cell RPC calls MUST go through generated typed clients, not raw stubs or hand-rolled HTTP clients; this is enforced via archtest.
- **FR-012**: The dependency rules from the GoCell constitution MUST hold — `kernel/` MUST NOT depend on the RPC stack; `cells/` MUST NOT depend on `adapters/grpc` directly (only via interface defined in `kernel/` or `runtime/`).
- **FR-013**: All new constraints introduced by this feature (proto path layout, kind=grpc schema, codegen golden, mount funnel, client funnel, archtest invariants) MUST be classified per the AI-robust ranking (Hard / Medium / Soft); Soft is forbidden per the AI-robust rule.
- **FR-014**: Delivery MUST be sliced into pull requests of strictly ≤ 2000 lines net (insertions + deletions per `git diff --numstat`), each of which is independently shippable and revertible.
- **FR-015**: For each PR, every `archtest` invariant introduced in that PR MUST land in the same PR (no "constraint added now, enforcement later") per the AI-robust governance rule and the contract-fanout rule.
- **FR-016**: No backward-compatibility shims (no deprecation aliases, no dual-path) — GoCell has no external consumers; transport choice flips cleanly.
- **FR-017**: The `verify.contract` discipline (one verifying test per `contractUsage`) MUST extend to RPC contracts.
- **FR-018**: RPC operations MUST integrate with the existing AuthPlan / Principal funnel; RPC endpoints requiring auth MUST be expressible declaratively without per-cell auth wiring.
- **FR-019**: Bidirectional and long-lived streams MUST integrate with the graceful-shutdown drain protocol; in-flight streams either complete within the drain window or receive a typed half-close before the listener shuts down.
- **FR-020**: Wire-format redaction (the equivalent of HTTP's 5xx Kind normalization + slog redact funnel) MUST be applied to RPC trailers / status; PII categories defined in `pkg/redaction` MUST be honoured on the RPC path identically to the HTTP path.

### Key Entities

- **RPC Contract** — a declarative record describing one or more RPC operations exposed by a cell, including service name, method name, request/response message shapes, error response set, streaming pattern, authorization requirements, and consistency-level mapping.
- **Generated Server Stub** — a per-cell artifact derived from RPC Contracts; defines the handler interface the cell implements and the registration call cellgen emits.
- **Generated Client Invoker** — a per-contract artifact consumed by callers; provides a typed function per operation, propagates context, returns `(response, *errcode.Error)`.
- **RPC Listener** — a transport-level component bound to a port, hosting the RPC handlers from one or more cells in an assembly. Analogous to an HTTP listener; shares the assembly's lifecycle hooks.
- **RPC Probe** — a typed readiness probe declared via the existing `ReadyProbeName` funnel; reports whether the listener can accept new RPC calls.
- **Proto Registry** — the namespace of declared proto packages + services + methods derived from `contract.yaml`; collision detection happens at generate time.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An author can take a cell from "no RPC operation" to "first unary operation reachable from a generated client" in a single working day, without touching any framework or transport code beyond business logic.
- **SC-002**: For any business operation, the metric / log / trace cardinality of the `cell` dimension is identical between RPC and HTTP — i.e., no series uses `cell="_runtime"` for an operation that has a known owning cell.
- **SC-003**: 100% of error responses returned by RPC handlers preserve the four-channel redaction (Message public / Details public / Internal absent on wire / Diagnostics in slog only) when audited end-to-end via a fixture.
- **SC-004**: Every delivery PR is ≤ 2000 lines of diff and ships independently — measured by counting violations across the implementation series: target zero.
- **SC-005**: Every new invariant introduced by this feature is classified Hard or Medium per the AI-robust ranking; Soft count is zero.
- **SC-006**: The total feature delivers within an estimated 8–12 PRs spread across 2–4 weeks of dev effort, with a clear "first usable RPC handler" milestone reached by PR 4 or earlier.
- **SC-007**: After all PRs ship, RPC and HTTP transports have parity on the 12 capability axes identified in the plan (typed envelope, errcode redaction, panic funnel, middleware chain, cell label, readyz, observability, principal, AuthPlan, archtest, contract/codegen, streaming/lifecycle).
- **SC-008**: Operator runbooks added by this feature describe RPC-specific incidents (port collision, stream stall, mTLS failure) with the same depth as existing HTTP runbooks.

## Assumptions

- **Pre-v1.0 evolution rule applies**: Wire contracts may evolve directly without v2 indirection during the pre-GA window (per existing ADR `202605211200-adr-pre-v1.0-direct-v1-evolution.md`). RPC contracts adopt the same rule.
- **Protocol Buffers v3 is the message format**. Proto2 is out of scope.
- **The reference Go RPC implementation (`google.golang.org/grpc` v1.x) is the chosen runtime**; alternative RPC frameworks (kitex with custom transport, hand-rolled RPC) are out of scope.
- **HTTP/2 with TLS is the standard production deployment**; plaintext mode is permitted only in dev / loopback profiles, gated behind explicit config.
- **`contracts/` remains the single source of truth for cross-cell boundaries**; proto file location lives under `contracts/grpc/{domain}/{version}/` parallel to existing `contracts/http/`.
- **codegen tool extensibility is available**: `tools/codegen` can host an RPC sub-generator with the same golden + regenerate-and-diff guarantees as the existing HTTP/OpenAPI generator. `buf` is the chosen proto compiler / breaking-change checker (vs raw `protoc`).
- **No new persistence schema** is required by the feature itself; cell-level state continues to live in each cell's repo. RPC is transport-only.
- **Assembly-level decision** on whether RPC and HTTP share a listener port (via H2C multiplexing) or run on separate listeners is deferred to plan phase, but the chosen pattern is uniform within an assembly.
- **No backwards-compatibility shims**: per GoCell project rules, all changes go in cleanly; deprecation aliases / dual paths are not introduced.
- **Each PR is independently buildable + testable + revertible**; no half-finished states cross PR boundaries.
- **Existing capabilities are immutable from RPC's perspective**: errcode, redaction, panic funnel, cell-attribution middleware, readyz funnel, codegen typed envelope, archtest framework, runtime auth — RPC adopts them, does not modify them. Where extension is required (e.g., new `errcode.Kind → codes.Code` mapping), it goes through the same governance discipline as any other framework extension.
