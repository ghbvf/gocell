# Research — gRPC Transport Adapter

Phase 0 output. Three parallel `explorer` agents executed in ship-style: (A) framework benchmarking, (B) capability alignment with the existing 12 axes, (C) PR slicing.

---

## §1 Framework Benchmark

### grpc-go (`google.golang.org/grpc`)

- `server.go` — `Server` struct (`services map[string]*serviceInfo`, `quit/done *grpcsync.Event`); `RegisterService(sd *ServiceDesc, ss any)` panics if called after `Serve`; `ChainUnaryInterceptor(...)` is nested-closure composition.
- `interceptor.go` — `UnaryServerInterceptor = func(ctx, req any, info *UnaryServerInfo, handler UnaryHandler) (any, error)`; recovery handler type `func(p any) error`; framework wraps recovery's error into a gRPC status.

**Key shape**: registration ↔ Serve lifecycle is order-dependent; interceptors are stateless function composition; no native cell/route dimension.

### kratos `transport/grpc/` (go-kratos/kratos)

- `server.go` — embeds `*grpc.Server`; `unaryServerInterceptor` injects context-merge + metadata + transport metadata before invoking Kratos middleware chain; `Start()/Stop()` drive `health.Resume()/Shutdown()`.
- `errors/errors.go` — `Error{Code int32, Message, Reason string, Metadata map[string]string}`; `GRPCStatus()` maps HTTP code → gRPC code via `httpstatus.ToGRPCCode()`, with `ErrorInfo` proto detail. **HTTP-primary error model**.
- `middleware/metadata/metadata.go` — `x-md-global-` / `x-md-local-` prefix injects metadata into ctx; configurable.

**Key shape**: HTTP & gRPC share one middleware interface `func(Handler) Handler`; health uses `grpc-health-v1` proto.

### kitex (`cloudwego/kitex`)

- `server/server.go` — `Server interface { RegisterService(*ServiceInfo, handler, opts...) error; Run(); Stop() }`; middleware ordering: timeout → server MW → service MW → core framework MW; middleware shape is `endpoint.Middleware = func(Endpoint) Endpoint`, **not** `grpc.UnaryServerInterceptor`.

**Key shape**: custom codec (Thrift/Protobuf/TTHeader); xDS mesh integration; bypasses grpc-go and builds own transport. **Not adoptable** — too much ecosystem coupling.

### go-zero `zrpc/` (zeromicro/go-zero)

- `zrpc/server.go` — `RpcServer{ server internal.Server, register internal.RegisterFn }`; `NewServer(c RpcServerConf, register RegisterFn)` splits Etcd publishing vs standalone; built-in interceptors: tracing / recovery / stats / prometheus / breaker / shedding / timeout.
- `zrpc/internal/rpcserver.go` — `grpc.ChainUnaryInterceptor(s.unaryInterceptors...)` + `grpc.ChainStreamInterceptor(...)`; health `grpc_health_v1.RegisterHealthServer(server, s.health)` + `health.Resume()`.

**Key shape**: `goctl` codegen produces server stub + client from `.proto`, register function injected via `RegisterFn` callback. **Closest match to GoCell's contractgen pattern** — direct inspiration for our grpc codegen.

---

## §2 Capability Alignment with GoCell's 12 axes

| # | Capability | Current HTTP locus | gRPC alignment | New mechanism? | New archtest? |
|---|-----------|--------------------|----------------|-----------------|---------------|
| 1 | typed envelope | `tools/codegen/contractgen/spec.go`: `ResponseSpec`, `GoTypeName`, generated `XxxResponseObject` | `GRPCResponseSpec` parallel; success → proto message; 4xx/5xx-equivalent → `google.golang.org/grpc/status` + `codes.Code` + `errdetails.ErrorInfo` | yes (`grpcutil.WriteStatus` + template) | yes (guard generated handler: no `return nil, errors.New(...)`) |
| 2 | errcode 3-layer redaction | `pkg/errcode/errcode.go`: `Error.project()` 5xx strips Details | gRPC trailer carries Details via `errdetails.ErrorInfo.Metadata`; 5xx-equivalent (`Internal/Unavailable/DeadlineExceeded`) strips Metadata; Internal never reaches wire | yes (`errcode.ToGRPCStatus`) | yes (5xx-equivalent codes must not carry details) |
| 3 | panic + Approved funnel | `runtime/http/middleware/recovery.go`: `recover()` + `recordPanicOnActiveSpan` + `httputil.WriteError` | grpc Unary/Stream Recovery interceptor; reuses `panicregister.Approved` + `redaction.RedactAny`; `codes.Internal` replaces HTTP 500 | yes (interceptor) | yes (`PANIC-REGISTERED-01` scope extension) |
| 4 | HTTP middleware chain | Recovery / Metrics / Tracing / AccessLog / RequestID / Auth / RateLimit / CircuitBreaker / BodyLimit / CORS / CSRF / SecurityHeaders | Recovery ✓ / Metrics ✓ / Tracing ✓ / AccessLog ✓ / RequestID ✓ / Auth ✓ / RateLimit ✓ / CircuitBreaker ✓ / BodyLimit→`grpc.MaxRecvMsgSize` ✓ ; **CORS / CSRF / SecurityHeaders N/A** (no browser surface) | yes (8 unary interceptors + chain) | yes (`GRPC-INTERCEPTOR-CHAIN-ORDER-01`; ban CORS on grpc) |
| 5 | cell label / attribution | `runtime/http/middleware/metrics.go` reads `ctxkeys.CellIDFrom`, falls back `_runtime` | derive cell from `FullMethod` (`/accesscore.AuthService/Login` → cellID) via registrar-maintained map; attribution interceptor injects `ctxkeys.CellID` | yes (method→cell registry) | yes (method MUST appear in contract.yaml) |
| 6 | readyz / healthz | `kernel/healthz.ReadyProbeName` typed-string funnel | new typed const `grpc_ready` in `adapters/grpc`; participates in same funnel | yes (`const ProbeReady healthz.ReadyProbeName = "grpc_ready"`) | yes (`OPS-CONTRACT-STRING-FUNNEL-01` golden inventory extends) |
| 7 | outbox / consumer | `kernel/outbox/`: transport-agnostic PG persistence | gRPC bypasses outbox (synchronous L1 calls); L2 events still emit via outbox from within RPC handler | no | yes (`GRPC-HANDLER-NO-DIRECT-OUTBOX-01` — no bypass) |
| 8 | AuthPlan / Principal | `kernel/auth/`: sealed `ListenerAuth` interface; JWT verifier transport-agnostic | gRPC metadata `authorization` header → token; new `kauth.AuthGRPC` implements `ListenerAuth`; Principal ctx-key identical | yes (`AuthGRPC`) | yes (`AUTH-PLAN-04` scope extension) |
| 9 | contract.yaml + codegen | `contracts/http/*/v1/contract.yaml` `kind: http`; `endpoints.http.method/path`; contractgen `ContractGenSpec.Kind ∈ {http, event}` | `kind: grpc`; new fields `grpc.{service, method, streamingType, proto}`; codegen generates server iface + client invoker + proto-to-DTO mapper | yes (large codegen extension) | yes (`GRPC-CONTRACT-CODEGEN-COMPLETE-01`) |
| 10 | observability (trace/log/metric) | W3C traceparent via HTTP headers; `request_id/correlation_id/trace_id` slog fields; `cell/method/route/status_code` metric labels | W3C traceparent via grpc metadata; same slog keys; new metric `grpc_server_requests_total{cell, method, code}` + `grpc_server_duration_seconds` | yes (observability interceptor + metric registration) | yes (`GRPC-METRIC-CELL-LABEL-01`) |
| 11 | Cell L0-L4 | `consistencyLevel` declared in `cell.yaml` | L semantics transport-agnostic; unary read = L0/L1, unary with outbox emit = L2; bidi-stream cross-cell coordination = L3/L4 | no | no (existing L2 archtest already transport-agnostic) |
| 12 | archtest | 628+ tests, 16-shard | minimum 6 new invariants (#1/#2/#3/#4/#5/#9 above) + `GRPC-HANDLER-NO-DIRECT-DB-01` + `PROTO-GEN-NO-HANDWRITE-01` + `GRPC-METHOD-IN-CONTRACT-01` | — | yes (6–8 new archtests) |

### High-risk alignment points

**Risk A — typed envelope (#1)**: HTTP envelope is status-code-driven (`Get200JSONResponse` / `Get404ErrorResponse`); gRPC has no status code. `codes.Code` set is not isomorphic to HTTP code set (e.g., `codes.NotFound` ↔ 404 but `codes.Gone` does not exist). Solution: design fresh `GRPCResponseSpec` with `code` enum field (`NOT_FOUND` / `ALREADY_EXISTS`) → `GoTypeName` mapping. Do **not** reuse `ResponseSpec.Status` integer field.

**Risk B — cell attribution (#5)**: HTTP cellID is encoded at `RouteGroup.CellID` register-time (static). gRPC requires runtime map `FullMethod → cellID`. The map's only legitimate SoR is `gocell generate contract` from `contract.yaml`. Hard-grade requires sealed registrar wrapper so manual registration becomes type-system-inexpressible. Most architecturally heavy item.

**Risk C — contract.yaml schema (#9)**: `.proto` file is itself a SoR for interface schema (service/method/message). Two-SoR drift risk between proto and contract.yaml. Solution chosen: contract.yaml is primary, proto file referenced via `grpc.proto` field; codegen parses proto at generate time and cross-validates service/method names. Archtest `GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01` enforces.

**Risk D — AuthPlan (#8)**: `auth.ListenerAuth` is sealed via `listenerAuthOK()` marker. `AuthGRPC` joins the sealed set; metadata extraction differs from HTTP header extraction; JWT verifier itself is transport-agnostic. Bootstrap's `WithListener` currently accepts HTTP-style `addr` string; new path needs `net.Listener` injection.

**Risk E — CORS/BodyLimit explicit N/A (#4)**: must be enforced — accidental application to grpc would be silent breakage. Archtest forbids `func(http.Handler) http.Handler`-signed middleware mounting on grpc interceptor chain.

### Alignment ordering

**First batch (foundation)**: #9 contract.yaml + codegen, #2 errcode mapping, #8 AuthGRPC
**Second batch (transport core)**: #5 cell attribution + method→cell map, #4 interceptor chain, #3 panic funnel
**Third batch (parity completion)**: #1 typed envelope finalisation, #10 observability, #6 readyz
**Always inline**: #12 archtest (with each batch)
**No work needed**: #7 outbox (only an archtest guard), #11 L-levels (no change)

---

## §3 errcode.Kind → codes.Code Mapping (ADR-bound in PR 12)

| `errcode.Kind` | gRPC `codes.Code` | Notes |
|---------------|-------------------|-------|
| `KindInternal` (zero value) | `codes.Internal` | fail-closed default |
| `KindInvalid` | `codes.InvalidArgument` | request validation |
| `KindUnauthenticated` | `codes.Unauthenticated` | |
| `KindPermissionDenied` | `codes.PermissionDenied` | |
| `KindNotFound` | `codes.NotFound` | |
| `KindConflict` | `codes.AlreadyExists` | |
| `KindGone` | `codes.NotFound` | gRPC has no Gone; degrades |
| `KindPayloadTooLarge` | `codes.ResourceExhausted` | |
| `KindRateLimited` | `codes.ResourceExhausted` | same code, distinguished by Reason |
| `KindDeadlineExceeded` | `codes.DeadlineExceeded` | |
| `KindUnavailable` | `codes.Unavailable` | |
| `KindNotImplemented` | `codes.Unimplemented` | |

**Departure from Kratos**: GoCell does NOT route through HTTP code as primary. `errcode.Error` is primary; `status.New(k.GRPCCode(), e.Message).WithDetails(ErrorInfo{Reason: e.Code, Metadata: {...}})` serialises directly. `errcode` remains single source.

**5xx redaction at wire**: in recovery / error-mapping interceptor, for `KindInternal | KindUnavailable | KindDeadlineExceeded`, strip `Details` (Metadata) — equivalent to HTTP 5xx strip.

---

## §4 PR Slicing — total 12 PRs / ~6500 lines / 2.5–3.5 weeks

See `plan.md` §"Phase 2 — Delivery Slicing" for the full 12-PR breakdown with file-level estimates. Summary of key milestones:

- **PR 1**: kernel + contractspec kind=grpc parsing (~440 lines, no deps)
- **PR 8** ⚡: first usable gRPC handler end-to-end (`examples/iotdevice`) (~560 lines)
- **PR 12** ⚡: full capability parity closure + ADR + governance + docs (~520 lines)

Two independent vertical slices converge at PR 7 (registrar wiring):
1. **codegen vertical**: PR 1 → PR 2 → PR 6
2. **runtime vertical**: PR 3 → PR 4 → PR 5

After PR 7, the path is linear: PR 8 (validation) → fan out into PR 9/10/11 → PR 12 (closure).

---

## §5 Decisions & Alternatives Rejected

| Decision | Choice | Rejected |
|----------|--------|----------|
| Proto compiler | `buf` | raw `protoc` (no breaking-change checks, manual plugin wiring) |
| Proto location | `contracts/grpc/{domain}/{version}/*.proto` | `proto/` root (parallel SoR, violates contracts/ ownership) |
| Codegen entrypoint | extend `tools/codegen/contractgen` | new `tools/codegen/grpcgen` (toolchain fragmentation) |
| HTTP+grpc port sharing | separate listeners | shared via `cmux` (added dep; failure domain entanglement) |
| Registration shape | `reg.GRPCService(...)` + bootstrap drain | direct `grpc.RegisterXxxServer` (breaks Init/Serve lifecycle; bypasses Registry) |
| errcode mapping | explicit table in `runtime/grpc/interceptor/errcode_mapping.go` | implicit `status.FromError` (silently maps unknown Kinds to `codes.Unknown`) |
| readyz probe | new typed const `grpc_ready` | reuse `http_ready` (different failure domain; masks gRPC outage) |
| Streaming PR scope | single PR (all 3 stream patterns) | per-pattern PRs (bidi requires server+client base anyway) |
| Streaming first or unary first | unary PR 8, streaming PR 10 | bundle them (PR 8 would exceed 2000-line cap) |
| Kernel-cell type safety for `GRPCServiceSpec` | `any` + archtest (Medium) | sealed interface (impossible to express in pre-PR-3 layering) |

---

## §6 Dependency Inventory

| Dep | Version | Rationale | Risk |
|-----|---------|-----------|------|
| `google.golang.org/grpc` | v1.64+ | Stable v1 path (v2 not GA); supports Go 1.21+ | v1→v2 migration deferred to future feature |
| `google.golang.org/protobuf` | v1.34+ | APIv2; locked to grpc-go v1.64 compat | legacy `github.com/golang/protobuf` (APIv1) MUST NOT be re-introduced |
| `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` | match current `adapters/otel` SDK | reuses existing TracerProvider | breaking changes in otel contrib track separately |
| `buf` CLI | v1.30+ | breaking-change linting; vendored in Makefile + CI | not a Go module dep; pinned via `buf.yaml` |

**kernel/ exposure verified**: extension lives in `kernel/contractspec` + `kernel/metadata` + `kernel/cell` — none directly import `google.golang.org/grpc`; the `*grpc.ServiceDesc` value flows as `any` through `GRPCServiceSpec`.
