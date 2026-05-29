# Implementation Plan: gRPC Transport Adapter

**Branch**: `515-grpc-adapter` | **Date**: 2026-05-26 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/515-grpc-adapter/spec.md`

## Summary

Introduce gRPC as a third transport protocol alongside HTTP (`runtime/http`) and AMQP (`kernel/outbox` + `ConsumerBase`). Cells declare RPC operations in `contract.yaml` with `kind: grpc`; codegen derives the server handler interface and typed client invoker. The new transport reuses the existing typed envelope discipline, errcode four-channel redaction, panic taxonomy + Approved funnel, cell-attribution label, readyz typed-string funnel, Principal/RequestID/correlation propagation, AuthPlan funnel, and archtest framework. Delivery is sliced into 12 PRs of strict ≤ 2000 lines net diff (code + tests + docs), each independently buildable, testable, revertible.

**Approach**: build the adapter and the codegen extension in two independent vertical slices that meet at PR 7 (registrar wiring); validate end-to-end at PR 8 in `examples/iotdevice/`; align the remaining 7 observability/streaming/governance axes through PR 12.

## Technical Context

**Language/Version**: Go (latest stable; current repo go.mod line)
**Primary Dependencies**:
- `google.golang.org/grpc` v1.x — official Go RPC server/client runtime (v2 module path not GA, pinned to v1)
- `google.golang.org/protobuf` v1.34+ — proto3 message runtime (APIv2; legacy `github.com/golang/protobuf` MUST NOT be re-introduced)
- `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` — version locked to current `adapters/otel` SDK
- `buf` CLI (build-time only, not a Go dep) — proto compiler + breaking-change checker; pinned in `Makefile` + CI
- No new runtime dep on `kernel/` (constraint: `kernel/` only depends on stdlib + `pkg/` + `gopkg.in/yaml.v3`)

**Storage**: N/A for this feature (transport-only; no new schema; existing cell repos unchanged)
**Testing**: stdlib `testing` + `testify`; table-driven tests for invariants; integration tests use in-process grpc dial (`bufconn`) — no testcontainers required for the runtime tests; archtest follows the existing `tools/archtest` typed loader
**Target Platform**: Linux server (same as HTTP); HTTP/2 with TLS standard production; H2C plaintext gated behind explicit `AllowInsecure` opt-in (not restricted to loopback; mesh-sidecar plaintext on non-loopback is a valid production posture)
**Project Type**: framework + adapter (single Go module; layered per GoCell constitution)
**Performance Goals**: unary RPC p99 ≤ HTTP+JSON p99 for equivalent operation (typically 30–50% lower wire size + parsing cost; not a feature gate, observable in SC-001)
**Constraints**:
- Each PR ≤ 2000 lines net diff (per `git diff --numstat`)
- All new constraints classified Hard or Medium; zero Soft (per AI-robust rule)
- `kernel/` MUST NOT depend on `google.golang.org/grpc`
- `cells/` MUST NOT depend on `adapters/grpc` (only via interface)
- No backwards-compatibility shims (pre-v1.0 direct evolution)

**Scale/Scope**:
- 12 PRs / ~6500 lines net diff total (±30%)
- 2.5–3.5 weeks single-developer / ~2 weeks two-developer parallel
- 6–8 new archtest invariants; touches 4 framework packages (`kernel/contractspec`, `tools/codegen/contractgen`, `runtime/bootstrap`, `kernel/cell/registrar`) + 2 new packages (`adapters/grpc`, `runtime/grpc/interceptor`)
- 1 example cell (iotdevice) end-to-end at PR 8; 1 platform cell slice (accesscore sessionrpc) at PR 11

## Constitution Check

GoCell project constitution (`.specify/memory/constitution.md`) gates re-evaluated post-design:

| Principle | Gate | Status |
|-----------|------|--------|
| **I. Cell-Native Layering** | `kernel/` ↛ `runtime/`/`adapters/`/`cells/`; `cells/` ↛ `adapters/grpc` | ✅ — `kernel/cell.GRPCServiceSpec` holds `any` (interface), grpc.ServiceDesc injected from adapter layer; archtest `GRPC-CELL-REGISTRAR-LAYER-01` (Medium) enforces |
| **II. Cell Governance + Six Truths** | contract.yaml is boundary truth | ✅ — `kind: grpc` is a kind extension; proto file is referenced from contract.yaml (`grpc.proto`), not a parallel SoR. archtest `GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01` (Hard) ensures no hand-written proto import paths in generated code |
| **III. Contract Boundary Discipline** | RPC contracts use existing kind taxonomy | ⚠️ — current closed set is `http \| event \| command \| projection`; this plan extends to add `grpc`. Constitution defines contract kinds in table at §III but the runtime closed set (`kernel/governance.builder.go`) is the enforced gate. **Decision**: extend the kind enumeration as a one-line addition; archtest `CONTRACT-KINDS-CLOSED-SET-01` updated in PR 1. No constitution amendment needed — `command` is the analogous precedent. Provider/consumer role pair for `grpc`: `server` / `clients`; provider role `serve`, consumer role `call` (mirrors `http`). |
| **IV. TDD non-negotiable** | every PR ships its tests | ✅ — each PR lists `*_test.go` first in the file list per PR section below |
| **V. Cell Data Sovereignty** | no schema change | ✅ — transport-only feature |
| **VI. Event-driven + L0-L4** | RPC L-level semantics | ✅ — unary RPC defaults to L1 (single-cell tx) or L2 (with outbox emit inside handler); streaming spec defers to PR 10. archtest `GRPC-HANDLER-NO-DIRECT-OUTBOX-01` ensures handlers don't bypass outbox for L2 events |
| **VII. Journey-driven Acceptance** | journey covers RPC paths | ⚠️ — at PR 12 add a journey `J-grpc-roundtrip.yaml` (auto passCriteria); first RPC handler (PR 8) gets smoke + contract verify, journey added at closure PR |
| **VIII. Security by Default** | AuthPlan + Principal + audit | ✅ — `kauth.AuthGRPC` extends sealed `ListenerAuth`; mTLS supported (FR-009); no plaintext by default (explicit `AllowInsecure` opt-in required; non-loopback plaintext valid for mesh-sidecar deployments) |
| **IX. Simplicity + Incremental Delivery** | YAGNI | ✅ — unary first, streaming PR 10; client retry policy deferred to future feature |

**Pre-v1.0 evolution rule** (ADR `202605211200`): wire contracts may evolve directly without v2 indirection. RPC contracts adopt the same rule, written in spec Assumptions.

**Red-line check**:
- RL-04 (legacy field names): no legacy fields introduced
- RL-06 (L2 fire-and-forget): archtest `GRPC-HANDLER-NO-DIRECT-OUTBOX-01` enforces
- RL-14 (fail-fast infra): no `noop` grpc server; PR 5 phase0 fails fast if `authChain == nil`
- RL-15 (internal API trust): internal-only RPC contracts go through declared `/internal` service-name prefix + network isolation, same as HTTP `/internal/v1/`

**No constitution violations require Complexity Tracking entries.**

## Project Structure

### Documentation (this feature)

```text
specs/515-grpc-adapter/
├── plan.md                  # This file
├── spec.md                  # Feature specification (business-level)
├── research.md              # Phase 0 output — explorer findings consolidated
├── data-model.md            # Phase 1 output — new framework types
├── quickstart.md            # Phase 1 output — first gRPC handler walkthrough
├── contracts/               # Phase 1 output — example grpc contract.yaml + proto
│   ├── example-unary-contract.yaml
│   └── example-unary.proto
└── checklists/
    └── requirements.md      # Spec quality validation
```

### Source Code (repository root)

```text
# New packages
adapters/grpc/
├── server.go                # grpc.Server lifecycle wrapper (PR 3)
├── config.go                # ServerConfig + TLS
├── readyz.go                # grpc_ready ReadyProbeName typed const (PR 9)
└── *_test.go

runtime/grpc/
├── interceptor/
│   ├── chain.go             # ordered chain composition (PR 4)
│   ├── recovery.go          # panic → panicregister.Approved (PR 4)
│   ├── tracing.go           # otelgrpc + safeStringAttr (PR 4)
│   ├── metrics.go           # grpc_server_* + cell label (PR 4, label PR 9)
│   ├── access_log.go        # slog structured (PR 9)
│   ├── auth.go              # JWT extract from metadata (PR 4)
│   ├── rate_limit.go        # reuse runtime/http/middleware limiter (PR 12)
│   ├── circuit_breaker.go   # reuse runtime/http/middleware breaker (PR 12)
│   ├── errcode_mapping.go   # errcode.Kind → codes.Code (PR 12)
│   ├── stream_*.go          # streaming variants (PR 10)
│   └── *_test.go
└── registrar.go             # ServiceRegistrar drain target (PR 7)

# Extended packages
kernel/contractspec/         # PR 1: kind=grpc, GRPCEndpointSpec
kernel/metadata/             # PR 1: contract.yaml grpc subtree parser
kernel/cell/                 # PR 7: GRPCService registrar method, GRPCServiceSpec (any-typed)
runtime/bootstrap/           # PR 5: WithGRPCListener option + phase5 drain
tools/codegen/contractgen/   # PR 2, 6: grpc kind generator + golden + proto refs
tools/archtest/              # PR 1, 3, 4, 5, 7, 9, 10, 11, 12: ~8 new invariants
pkg/errcode/                 # PR 12: optional grpc.go for ToGRPCStatus (or place in runtime/grpc/interceptor)

# New contract paths
contracts/grpc/{domain}/{version}/contract.yaml
contracts/grpc/{domain}/{version}/*.proto
generated/contracts/grpc/{domain}/{version}/*_gen.go  # codegen products; not hand-edited
generated/contracts/grpc/{domain}/{version}/*.pb.go   # protoc products; not hand-edited

# Example + platform cells
examples/iotdevice/contracts/grpc/...         # PR 8
examples/iotdevice/cells/iotdevice/slices/devicecommand/   # PR 8
cells/accesscore/slices/sessionrpc/           # PR 11

# Docs
docs/architecture/202605260000-adr-grpc-transport-adapter.md  # PR 1
docs/architecture/202605260100-adr-grpc-errcode-mapping.md    # PR 12
docs/guides/grpc-development-guide.md                          # PR 12
docs/references/framework-comparison.md                        # PR 12 (extend)
```

**Structure Decision**: dual-package split (`adapters/grpc` for transport runtime; `runtime/grpc/interceptor` for cross-cutting interceptors) mirrors the existing HTTP layout (`adapters/postgres` vs `runtime/http/middleware`). Codegen extension lives inside `tools/codegen/contractgen` rather than a new package, to preserve the single `gocell generate` entrypoint.

---

## Phase 0 — Research Consolidation

Phase 0 ran three parallel explorer agents (ship-style). Findings are consolidated in [research.md](./research.md). Summary of decisions:

| Decision | Choice | Rationale | Alternatives rejected |
|----------|--------|-----------|----------------------|
| RPC runtime | `google.golang.org/grpc` v1.x | Industry standard; otelgrpc/buf ecosystem mature; pre-v2 path stable | kitex (vendor lock-in to ByteDance ecosystem; bespoke transport); rolling own RPC (massive scope creep) |
| Proto compiler | `buf` CLI | Breaking-change detection (parallels HTTP schema evolution ADR 202605031600); modern toolchain | raw `protoc` (no breaking checks, manual plugin wiring) |
| Proto file location | `contracts/grpc/{domain}/{version}/*.proto` | Parallel to existing `contracts/http/`; preserves single-SoR rule | `proto/` root (parallel SoR, breaks contracts/ ownership) |
| Codegen pattern | Extend `tools/codegen/contractgen` with `kind=grpc` branch | Single entrypoint preserved; reuse spec/builder/render layering; golden + regenerate-and-diff applies | New `tools/codegen/grpcgen` (parallel toolchain to maintain) |
| Service registration | `reg.GRPCService(...)` parallel to `reg.RouteGroup`; bootstrap phase5 drain | Matches existing Registry builder pattern; lifecycle-safe (Init declares intent, bootstrap mounts) | grpc-go-native `RegisterXxxServer` direct call (bypasses Registry; breaks Init/Serve lifecycle); Kratos-style transport-merge ListenerAuth (more invasive) |
| Cell attribution source | grpc `FullMethod` → cellID map maintained by codegen-generated `RegisterXxxServer` wrapper | Single source from contract.yaml; archtest `GRPC-METHOD-IN-CONTRACT-01` enforces | Manual map (two SoRs); ctxkey-only (no compile-time guarantee) |
| errcode → grpc/codes mapping | Explicit table (see §research.md §3) inside `runtime/grpc/interceptor/errcode_mapping.go`; ADR-fixed | Removes ambiguity; archtest `GRPC-ERRCODE-MAPPING-01` ensures exhaustive coverage | Implicit `status.FromError` fallback (silently maps unknown Kinds to `Unknown`) |
| readyz probe shape | New typed const `grpc_ready` via `kernel/healthz.ReadyProbeName` funnel | Mirrors `postgres_ready`, `rabbitmq_ready`; no naming improvisation | Reuse `http_ready` (different failure domain; would mask gRPC outage when HTTP healthy) |
| Streaming initial scope | PR 10: server-stream + client-stream + bidi all together (after unary stable) | One streaming PR is testable as a unit; deferring further fragments the design | Split per pattern (PR fragmentation; bidi needs server+client base anyway) |
| 4 RPC patterns transport sharing | Same listener for unary + streaming (HTTP/2 multiplexing native) | Stock grpc-go behavior; no separation gain | Separate listeners (operational complexity) |
| HTTP/gRPC listener port | Separate listeners (default); H2C multiplexing deferred | Operationally simpler; failure isolation; matches K8s service-per-protocol convention | Shared port via cmux (added dependency; failure domain entanglement) |
| Middleware not applicable to gRPC | CORS, BodyLimit (HTTP semantics), CSRF, SecurityHeaders | gRPC framing not browser-exposed; size limit configured via `grpc.MaxRecvMsgSize` option | Adapter layer to bridge HTTP middleware → grpc interceptor (false equivalence; surface area sprawl) |
| Panic handling | Same `panicregister.Approved` funnel; Recovery interceptor reuses kernel funnel | Single panic taxonomy across transports | Bypass funnel for grpc (creates parallel taxonomy; breaks `PANIC-REGISTERED-01` invariant) |

**All [NEEDS CLARIFICATION] markers in spec resolved** by these decisions (zero unresolved).

---

## Phase 1 — Design & Contracts

Phase 1 artifacts (separate files):
- [data-model.md](./data-model.md) — new framework types: `GRPCEndpointSpec`, `GRPCServiceSpec`, `ProtoRegistry`, `RpcKind` enum extension
- [quickstart.md](./quickstart.md) — "add your first gRPC handler" walkthrough using `examples/iotdevice`
- [contracts/example-unary-contract.yaml](./contracts/example-unary-contract.yaml) — illustrative `kind: grpc` contract.yaml
- [contracts/example-unary.proto](./contracts/example-unary.proto) — illustrative proto file

Constitution Check post-design re-evaluation: **all gates still pass** (no scope creep introduced by design).

---

## Phase 2 — Delivery Slicing (12 PRs)

Each PR is independently buildable, testable, revertible. Strict ≤ 2000-line net diff cap. Critical-path PRs marked ⚡.

### Dependency graph

```
PR1 (kernel kind parse) ── PR2 (codegen stub) ── PR6 (codegen real proto) ─┐
                                                                          │
PR3 (adapters/grpc server) ── PR4 (interceptors) ── PR5 (bootstrap wiring)─┤
                                                                          │
                                                                  PR7 (registrar) ⚡
                                                                          │
                                                                  PR8 (examples) ⚡ ← milestone
                                                                          │
                                                       ┌──────────────────┼──────────────┐
                                                       │                  │              │
                                                  PR9 (observability) PR10 (streaming) PR11 (platform cell)
                                                       │                  │              │
                                                       └──────────────────┴──────PR12 (closure) ⚡
```

| PR | Direct deps | Est. lines | Parallelizable with |
|----|-------------|------------|---------------------|
| 1 | — | 440 | 3 |
| 2 | 1 | 530 | 3, 4 |
| 3 | — | 650 | 1, 2 |
| 4 | 3 | 860 | 2 |
| 5 | 3, 4 | 430 | 6 |
| 6 | 2 | 400 | 5 |
| 7 | 5, 6 | 500 | — |
| 8 | 7 | 560 | — |
| 9 | 4, 5, 8 | 540 | 10, 11 |
| 10 | 9 | 630 | 11 |
| 11 | 8 | 400 | 9, 10 |
| 12 | 11 | 520 | — |
| **Total** | | **~6 460 ± 30%** | |

### PR 1 — kernel kind=grpc parse + contractspec extension

- **Scope**: `kernel/metadata/contract.go` accepts `kind: grpc` and the `grpc:` subtree (service/method/stream/proto fields); `kernel/contractspec.ContractSpec` gains transport-specific fields; ADR `202605260000-adr-grpc-transport-adapter.md` records the decision.
- **Files**:
  - `kernel/metadata/contract.go` ~80
  - `kernel/metadata/contract_test.go` ~120
  - `kernel/contractspec/contractspec.go` ~60
  - `kernel/contractspec/contractspec_test.go` ~80
  - `docs/architecture/202605260000-adr-grpc-transport-adapter.md` ~100
- **TDD**: `contract_test.go` table-driven for grpc kind parse (valid/invalid); `contractspec_test.go` for `ContractSpec.GRPCInfo()` accessor & validation boundaries.
- **archtest (same PR)**:
  - `CONTRACT-KINDS-CLOSED-SET-01` — extend closed set to include `grpc` (Hard: governance builder.go switch exhaustive)
  - `GRPC-KIND-PARSE-01` — `kind=grpc` requires `grpc.service` + `grpc.method` non-empty (Medium: AST scan of decode logic)
- **Est.**: 440 lines
- **Risk**: downstream exhaustive `switch contract.Kind` callsites — grep-verified at PR-1 time; any missed case is a same-PR fix (no follow-up PR).

### PR 2 — contractgen grpc placeholder stub (no proto dep yet)

- **Scope**: `tools/codegen/contractgen` learns a grpc branch that emits a placeholder server interface using `[]byte` for request/response (no proto import); golden file records expected output.
- **Files**:
  - `tools/codegen/contractgen/spec.go` (+`GRPCEndpointSpec` type) ~60
  - `tools/codegen/contractgen/builder.go` (+grpc kind branch) ~80
  - `tools/codegen/contractgen/render.go` (+grpc template dispatch) ~40
  - `tools/codegen/contractgen/templates/grpc_server.tmpl` ~120
  - `tools/codegen/contractgen/generator_test.go` (+golden) ~150
  - `tools/codegen/contractgen/testdata/grpc/` ~80
- **TDD**: golden-file test for emitted Go source (byte-exact)
- **archtest**:
  - `GRPC-CODEGEN-NO-PROTO-DEP-01` (PR-2 only; removed in PR 6) — `generated/contracts/grpc/**/*.go` MUST NOT import `google.golang.org/protobuf` (Hard: AST import scan)
- **Est.**: 530 lines
- **Risk**: golden regeneration command must be discoverable (`Makefile` `codegen-golden` target updated in this PR).

### PR 3 — adapters/grpc server (lifecycle + TLS)

- **Scope**: new package `adapters/grpc` with `Server`, `ServerConfig`, `Serve(ctx)`, `GracefulStop()`; TLS config from existing `kernel/crypto` patterns; ManagedResource interface implementation (matches `adapters/postgres` lifecycle).
- **Files**:
  - `adapters/grpc/server.go` ~200
  - `adapters/grpc/config.go` ~80
  - `adapters/grpc/doc.go` ~20
  - `adapters/grpc/server_test.go` ~200
  - `adapters/grpc/server_integration_test.go` ~150 (bufconn in-proc, no testcontainers)
- **TDD**: unit (fake listener) + integration (bufconn dial); GracefulStop honours ctx cancel within drain budget
- **archtest**:
  - `GRPC-ADAPTER-LAYER-01` — `adapters/grpc/` MUST NOT import `cells/` or `runtime/grpc/interceptor/` (Hard: typed import scan)
- **Est.**: 650 lines
- **Risk**: `KeepaliveParams` × `GracefulStop` interaction; addressed by reference to grpc-go `server.go` and `bufconn` test isolation.

### PR 4 — interceptor chain (unary): Recovery + Metrics + Tracing + Auth

- **Scope**: `runtime/grpc/interceptor` with 4 unary interceptors + `chain.go` composition. AccessLog and the cell-label injection are deferred to PR 9 (their input data — request-scoped attribution + slog field set — is unstable until cells declare grpc services in PR 7).
- **Files**:
  - `runtime/grpc/interceptor/tracing.go` ~120 (otelgrpc + `safeStringAttr` reuse)
  - `runtime/grpc/interceptor/metrics.go` ~100 (`grpc_server_requests_total` registration; cell label injection deferred)
  - `runtime/grpc/interceptor/recovery.go` ~80 (`panicregister.Approved` + `redaction.RedactAny`)
  - `runtime/grpc/interceptor/auth.go` ~100 (extract `authorization` metadata key; reuse JWT verifier)
  - `runtime/grpc/interceptor/chain.go` ~60
  - `runtime/grpc/interceptor/*_test.go` ~400
- **TDD**: each interceptor unit-tested with mocked `UnaryServerInfo` + `UnaryHandler`; chain composition test
- **archtest**:
  - `SPAN-SETATTR-REDACT-01` — extend A2 callsite coverage to include `runtime/grpc/interceptor/tracing.go`
  - `PANIC-REGISTERED-01` — extend caller-set to include `runtime/grpc/interceptor/recovery.go::repanicInGRPC`
  - `GRPC-INTERCEPTOR-CHAIN-ORDER-01` — recovery MUST be outermost, auth MUST follow tracing+metrics+recovery (Medium: AST scan of `ChainUnaryInterceptor(...)` arg order)
- **Est.**: 860 lines
- **Risk**: `auth` interceptor must not leak HTTP-listener-auth assumptions; the JWT verifier is transport-agnostic, but the token extraction (HTTP header vs grpc metadata) MUST be isolated. Addressed by typed `tokenSource` interface.

### PR 5 — bootstrap WithGRPCListener + phase5 drain

- **Scope**: `runtime/bootstrap` learns `WithGRPCListener(addr string, authChain auth.ListenerAuth, opts ...adaptersgrpc.ServerOption) Option`; phase 0 fails fast if `authChain == nil` (RL-14 fail-closed); phase 5 starts grpc server in parallel to HTTP via `errgroup`; phase 7 GracefulStop in symmetry with HTTP shutdown.
- **Files**:
  - `kernel/cell/listener.go` (+`GRPCListener` listener kind constant) ~30
  - `runtime/bootstrap/grpc_option.go` ~120
  - `runtime/bootstrap/bootstrap.go` (+phase5 grpc drain + phase7 shutdown) ~80
  - `runtime/bootstrap/grpc_option_test.go` ~150
  - `cmd/corebundle/` (+example wiring guarded by build tag) ~50
- **TDD**: phase0 fail-fast on `authChain=nil`; happy path Serve → GracefulStop; HTTP+grpc concurrent serve (errgroup propagation)
- **archtest**:
  - `GRPC-LISTENER-PHASE0-01` — `WithGRPCListener(_, nil, _)` → phase0 error (Hard: bootstrap phase0 fail-fast assertion test)
  - `AUTH-PLAN-04` — extend scope to grpc listener registration
- **Est.**: 430 lines
- **Risk**: bootstrap is the most complex lifecycle code in repo (phase0-phase7); regression risk addressed by mirroring HTTP listener wiring shape exactly, no novel sequencing.

### PR 6 — codegen real proto integration (replaces placeholder)

- **Scope**: replace PR 2's `[]byte` placeholders with real proto-typed `pb.Request` / `pb.Response`; introduce `ProtoRegistry` in contractgen mapping `contractID → .proto path`; `go.mod` introduces `google.golang.org/grpc` + `google.golang.org/protobuf`.
- **Files**:
  - `go.mod` / `go.sum` ~20
  - `tools/codegen/contractgen/spec.go` (+`ProtoPackage`/`ProtoService` fields) ~60
  - `tools/codegen/contractgen/builder.go` (+proto ref resolution + collision detection) ~80
  - `tools/codegen/contractgen/templates/grpc_server.tmpl` (+real proto types) ~60
  - `tools/codegen/contractgen/generator_test.go` (+update golden) ~100
  - `tools/codegen/contractgen/testdata/grpc/*.proto` ~80
- **TDD**: golden update; negative test (proto path missing → builder error)
- **archtest**:
  - Remove `GRPC-CODEGEN-NO-PROTO-DEP-01` (no longer applies)
  - `GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01` (Hard: proto import paths in generated code MUST come from ProtoRegistry; no hand-written `import "google.golang.org/.../pb"` strings — AST scan)
- **Est.**: 400 lines
- **Risk**: introducing `google.golang.org/grpc` to go.mod triggers layered import scans; PR-6 same-PR audit confirms `kernel/` transitively unpolluted.

### PR 7 — ServiceRegistrar + Cell.GRPCService API ⚡

- **Scope**: new `runtime/grpc/registrar.go` `ServiceRegistrar.RegisterService(desc, impl)`; `kernel/cell.Registrar` gains `GRPCService(spec GRPCServiceSpec, impl any)`; bootstrap phase5 drains `RegistrySnapshot.GRPCServices` to registrar.
- **Files**:
  - `runtime/grpc/registrar.go` ~150
  - `runtime/grpc/registrar_test.go` ~150
  - `kernel/cell/registrar.go` (+`GRPCService` method) ~60
  - `kernel/cell/registry.go` (+`GRPCServiceSpec` `any`-typed) ~60
  - `runtime/bootstrap/bootstrap.go` (+drain `GRPCServices`) ~80
- **TDD**: register-then-call happy path; duplicate service desc → fail-fast; cellID resolution from `GRPCServiceSpec.CellID`
- **archtest**:
  - `GRPC-CELL-REGISTRAR-LAYER-01` (Medium: `kernel/cell.GRPCServiceSpec` holds `interface{}` for `*grpc.ServiceDesc`; `kernel/` MUST NOT import `google.golang.org/grpc` — AST + import scan)
- **Est.**: 500 lines
- **Risk**: `kernel/` ↛ grpc constraint enforced via `any`-typed field, with runtime type assertion in the adapter layer. Upgrade path to type-safe sealed interface tracked in a follow-up gh issue (Hard upgrade per AI-robust §Funnel 双向锁评级).

### PR 8 — examples/iotdevice gRPC handler (first end-to-end) ⚡

- **Scope**: an `iotdevice` slice (`devicecommand/`) exposes one unary RPC operation `IssueCommand(deviceID, command) → CommandAck`. End-to-end traffic: client dial → interceptor chain → handler → returns ack; observability surfaces in `/metrics` with correct cell label.
- **Files**:
  - `examples/iotdevice/contracts/grpc/device/command/v1/contract.yaml` ~40
  - `examples/iotdevice/contracts/grpc/device/command/v1/device_command.proto` ~50
  - `generated/contracts/grpc/device/command/v1/server_gen.go` (codegen product) ~120
  - `generated/contracts/grpc/device/command/v1/device_command.pb.go` (buf product) ~150
  - `examples/iotdevice/cells/iotdevice/slices/devicecommand/handler.go` ~120
  - `examples/iotdevice/cells/iotdevice/slices/devicecommand/handler_test.go` ~150
  - `examples/iotdevice/cells/iotdevice/cell.go` (+GRPCService reg) ~40
  - `examples/iotdevice/cmd/main.go` (+WithGRPCListener) ~40
- **TDD**: handler unit (direct call); cell integration (bufconn dial + Invoke)
- **archtest**:
  - `LAYER-06` — extend cell-owned subpackage scope to grpc slice (consistency check)
  - `GRPC-METHOD-IN-CONTRACT-01` (Hard: registered grpc methods MUST appear in contract.yaml — generated registrar derives the map, no hand-registration)
- **Est.**: 560 lines
- **Risk**: this is the integration acid test — any earlier API gap surfaces here. Buffer reserved for same-PR API refinement (max 1–2 packages re-touched without exceeding 2000-line cap).

### PR 9 — observability parity: cell label + readyz probe + AccessLog

- **Scope**: complete metrics cell label injection; new `grpc_ready` readiness probe; AccessLog interceptor; full slog field set (`cell`, `method`, `code`, `duration_ms`, `request_id`, `correlation_id`, `trace_id`).
- **Files**:
  - `runtime/grpc/interceptor/metrics.go` (+cell label injection finalised) ~80
  - `runtime/grpc/interceptor/access_log.go` ~80
  - `adapters/grpc/readyz.go` (+`ProbeReady healthz.ReadyProbeName = "grpc_ready"`) ~80
  - `runtime/grpc/interceptor/metrics_test.go` ~120
  - `runtime/grpc/interceptor/access_log_test.go` ~100
  - `adapters/grpc/readyz_test.go` ~80
- **TDD**: metrics test verifies cell label present + correct fallback to `_runtime` for unknown methods; readyz test verifies status transitions; access_log test verifies redaction pass-through
- **archtest**:
  - `OPS-CONTRACT-STRING-FUNNEL-01` — extend golden inventory to include `grpc_ready`
  - `GRPC-METRIC-CELL-LABEL-01` (Hard: cell label value MUST come from cell-attribution interceptor context, never from hand-written literal — typed source check)
- **Est.**: 540 lines
- **Risk**: cell label fallback to `_runtime` for framework grpc paths (e.g., grpc-health-v1 native service) — explicitly tested in PR 9.

### PR 10 — streaming (server-stream + client-stream + bidi)

- **Scope**: stream-variant interceptors; `ServiceRegistrar` supports streaming service descs; `examples/iotdevice` gains a server-stream handler `WatchCommands(filter) → stream Event` validating end-to-end.
- **Files**:
  - `runtime/grpc/interceptor/stream_tracing.go` ~100
  - `runtime/grpc/interceptor/stream_metrics.go` ~100
  - `runtime/grpc/interceptor/stream_recovery.go` ~80
  - `runtime/grpc/interceptor/wrapped_stream.go` (`grpc.ServerStream` wrapper for stream interceptors) ~100
  - `runtime/grpc/interceptor/stream_*_test.go` ~200
  - `examples/iotdevice/cells/iotdevice/slices/devicecommand/watch_handler.go` ~100
  - `examples/iotdevice/cells/iotdevice/slices/devicecommand/watch_handler_test.go` ~80 (drain on shutdown verified)
- **TDD**: stream interceptors unit; cell shutdown drain test (stream half-closes with typed reason)
- **archtest**:
  - `GRPC-RECOVERY-PANIC-APPROVED-01` — extend to stream interceptor re-panic sites
  - `GRPC-STREAM-DRAIN-01` (Medium: streaming handlers MUST respect `ctx.Done()` and emit typed half-close — AST scan of handler signatures + drain protocol assertion)
- **Est.**: 630 lines
- **Risk**: `wrappedStream` must implement 10+ `grpc.ServerStream` methods correctly; test matrix verifies each method passes through context + interceptor metadata.

### PR 11 — cells/accesscore platform RPC slice

- **Scope**: `cells/accesscore/slices/sessionrpc/` exposes session-token verification as an internal gRPC service for service-to-service calls; validates that a platform cell (not just example) integrates cleanly.
- **Files**:
  - `contracts/grpc/access/session/verify/v1/contract.yaml` ~40
  - `contracts/grpc/access/session/verify/v1/session_verify.proto` ~30
  - `cells/accesscore/slices/sessionrpc/handler.go` ~150
  - `cells/accesscore/slices/sessionrpc/handler_test.go` ~150
  - `cells/accesscore/slices/sessionrpc/slice.yaml` ~30
- **TDD**: handler unit + contract verify (`contract.grpc.access.session.verify.v1`)
- **archtest**:
  - `GRPC-CELL-REGISTRAR-LAYER-01` — confirm cells/ does NOT import `adapters/grpc` (Medium, scope extension)
- **Est.**: 400 lines
- **Risk**: real platform cell tests the `adapters/` decoupling more aggressively than examples; any leakage surfaces here.

### PR 12 — closure: errcode mapping + remaining middleware + governance + docs ⚡

- **Scope**: explicit `errcode.Kind → codes.Code` mapping (ADR-fixed table); RateLimit + CircuitBreaker interceptors; governance rule FMT extension for `kind: grpc`; closure docs.
- **Files**:
  - `runtime/grpc/interceptor/errcode_mapping.go` ~100
  - `runtime/grpc/interceptor/errcode_mapping_test.go` (table-driven exhaustive) ~120
  - `runtime/grpc/interceptor/circuit_breaker.go` ~80
  - `runtime/grpc/interceptor/rate_limit.go` ~60
  - `kernel/governance/` (FMT-NN rule for `kind: grpc`) ~80
  - `docs/architecture/202605260100-adr-grpc-errcode-mapping.md` ~80
  - `docs/guides/grpc-development-guide.md` ~100
  - `docs/references/framework-comparison.md` (+grpc section) ~40
  - `journeys/J-grpc-roundtrip.yaml` ~30 (auto passCriteria; addresses spec.md User Story 1 acceptance)
- **TDD**: errcode mapping table-driven (every `errcode.Kind` covered); circuit breaker interceptor (breaker open → `codes.Unavailable`)
- **archtest**:
  - `EXPORTED-ERROR-NEW-01` — extend scope to `runtime/grpc/`
  - `GRPC-ERRCODE-MAPPING-01` (Hard: `errcode.Kind → codes.Code` mapping MUST be exhaustive — codegen-style golden + reflect-based enum coverage assertion)
- **Est.**: 520 lines
- **Risk**: errcode mapping is irreversible once shipped (clients depend on status codes); fixed via ADR before PR 12 land; full table reviewed by reviewer agent at PR-12 review.

---

### Highest-risk PRs

1. **PR 5 (bootstrap wiring)** — Bootstrap.Run is the most complex lifecycle code in repo. Mitigation: mirror HTTP listener wiring shape exactly; no novel phase sequencing; same-PR regression test against existing bootstrap test suite.
2. **PR 7 (kernel/cell GRPCServiceSpec)** — `kernel/` ↛ grpc enforced via `any` field rather than type system. Mitigation: archtest `GRPC-CELL-REGISTRAR-LAYER-01` (Medium) + gh issue tracking Hard upgrade (sealed interface via private constructor in `adapters/grpc` once feasible).
3. **PR 12 (errcode mapping)** — wire-side semantics fixed; future client migrations expensive. Mitigation: ADR `202605260100` reviewed before PR 12 lands; explicit table; exhaustiveness archtest.

### Milestones

- **PR 4 complete**: framework grpc transport runtime functional in isolation (no codegen integration yet)
- **PR 8 complete**: ⚡ FIRST USABLE gRPC HANDLER end-to-end (spec User Story 1 acceptance: cell author can ship a unary RPC)
- **PR 9 complete**: spec User Story 2 acceptance (operator observability parity)
- **PR 11 complete**: spec User Story 3 acceptance (platform-cell service-to-service)
- **PR 10 complete**: spec User Story 4 acceptance (streaming patterns)
- **PR 12 complete**: ⚡ FULL CAPABILITY PARITY closure (all 12 axes aligned)

---

## Complexity Tracking

No constitution violations require justification.

One **funnel-pair Medium → Hard upgrade** is tracked as a follow-up rather than blocking this feature:

| Item | Why Medium now | Path to Hard | Tracking |
|------|---------------|--------------|----------|
| `GRPC-CELL-REGISTRAR-LAYER-01` | `kernel/cell.GRPCServiceSpec` field is `any`; archtest enforces `kernel/` ↛ grpc, but a kernel-internal struct could still hold a `*grpc.ServiceDesc` indirectly | Introduce a sealed interface in `adapters/grpc` with private constructor; `kernel/cell` references only the interface; archtest upgrade to Hard form (single sanctioned holder) | gh issue (to open at PR 7 merge) |

This matches AI-robust §Hard 范本目录 "single sanctioned holder" — currently Medium because upgrade path requires the adapter layer to exist (PR 3+).

---

## Tasks (deferred to `/speckit.tasks`)

This plan does **not** produce `tasks.md` — that is the `/speckit.tasks` output. Each of the 12 PRs above maps to one task group; intra-PR tasks (file-by-file TDD-first ordering) are generated by `/speckit.tasks` against this plan.
