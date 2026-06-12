# Data Model — gRPC Transport Adapter

Phase 1 output. New framework types introduced by this feature, organised by package.

> No persistence schema is added. All entities are in-memory framework types or codegen artefacts.

---

## `kernel/contractspec`

### `RpcKind` (string enum, extended)

```go
// Current closed set: "http" | "event" | "command" | "projection"
// PR 1 extends to: + "grpc"
type RpcKind string

const (
    KindHTTP       RpcKind = "http"
    KindEvent      RpcKind = "event"
    KindCommand    RpcKind = "command"
    KindProjection RpcKind = "projection"
    KindGRPC       RpcKind = "grpc"  // NEW
)
```

### `ContractSpec` (extended)

```go
type ContractSpec struct {
    ID        string
    Kind      RpcKind
    Transport string

    // existing fields unchanged

    // NEW: populated when Kind == KindGRPC
    GRPC *GRPCEndpointSpec
}
```

### `GRPCEndpointSpec` (new)

```go
type GRPCEndpointSpec struct {
    Service       string            // proto fully-qualified service name, e.g. "device.command.v1.DeviceCommandService"
    ProtoFile     string            // contracts-relative path, e.g. "contracts/grpc/device/command/v1/device_command.proto"
    ProtoPackage  string            // proto file's `package` declaration, e.g. "device.command.v1"
}
```

> `Method`, `StreamingType`, and the per-RPC `Auth` overlay were removed in #1655
> (service-level granularity, ADR D5). The method set and streaming kinds derive
> from the .proto file (single source of truth); per-method auth is deferred to
> #1675.

**Invariants**:
- `Service` MUST be non-empty when `Kind == KindGRPC` (validated in PR 1).
- `ProtoFile` MUST resolve to a file inside `contracts/grpc/`; absolute paths forbidden.
- `(ProtoPackage, Service)` pair MUST be globally unique across all contracts in the repo (proto registry collision check at codegen time).

---

## `kernel/metadata`

### `ContractMeta` (extended)

```go
type ContractMeta struct {
    // existing fields unchanged
    Kind RpcKind

    // NEW
    GRPC *GRPCContractMeta
}

type GRPCContractMeta struct {
    Service string
    Proto   string  // relative path, validated against contracts/grpc/ tree
}
```

YAML shape:

```yaml
id: grpc.device.command.v1.DeviceCommandService
kind: grpc
endpoints:
  server: iotdevice
  clients: []
  grpc:
    service: device.command.v1.DeviceCommandService
    proto: contracts/grpc/device/command/v1/device_command.proto
# Note: the grpc block lives under `endpoints.grpc` (parallel to endpoints.http).
# `method`, `streamingType`, and per-RPC `auth.public` removed in #1655 (ADR D5,
# service-level granularity). The contract owns the whole proto service; the method
# set derives from the .proto file. Per-method auth is deferred to #1675.
```

---

## `kernel/cell`

### `GRPCServiceSpec` (new)

**As-built (PR-7 #1150): Form B (callback).** The spec carries a `Register` callback —
`func(grpc.ServiceRegistrar)` held as `any` — instead of a `(ServiceDesc, Impl)` pair. This is
isomorphic with GoCell's own `RouteGroup.Register func(mux) error` and matches the go-zero
`RegisterFn func(*grpc.Server)` / Kratos `pb.RegisterXxxServer(srv, impl)` idiom: the cell supplies
a closure that calls the generated `pb.RegisterXxxServer` helper. Plus a `Listener` ref for
per-listener routing (symmetric with `RouteGroup.Listener`).

```go
type GRPCServiceSpec struct {
    ContractID string
    CellID     string
    Listener   ListenerRef // target gRPC listener; matches a WithGRPCListener(ref, …)
    Register   any         // expected func(grpc.ServiceRegistrar); asserted in runtime/grpc
}
```

> **AI-robust — permanent ceiling, not a feasible upgrade.** `Register` is `any` because
> `kernel/ ⊥ grpc` makes naming `func(grpc.ServiceRegistrar)` impossible. The earlier "sealed
> marker interface in adapters/grpc" idea is **infeasible** (kernel ⊥ adapters; an exported kernel
> marker seals nothing; an unexported kernel marker can't be implemented by cell/adapter closures)
> — same family as #851/#893/#1282. `GRPC-CELL-REGISTRAR-LAYER-01` is Medium (reflect field-lock) +
> the existing kernel⊥grpc depguard/`kernel_internal_dag_test.go` gate. Won't-do tracked at **#1582**.

`kernel/cell` also defines the narrow `GRPCServiceRegistrar interface { Register(GRPCServiceSpec) error }`
(bottom-of-graph home so both `adapters/grpc` and `runtime/bootstrap` import it without a cycle);
`*runtime/grpc.ServiceRegistrar` satisfies it structurally.

### `Registrar` (extended)

```go
type Registrar interface {
    // existing methods unchanged
    RouteGroup(prefix string, cellID string, fn func(*RouteMux))
    Subscribe(spec contractspec.ContractSpec, handler outbox.EntryHandler, group, cellID string) error
    // ... etc

    // NEW (PR 7)
    GRPCService(spec GRPCServiceSpec) error
}
```

### `RegistrySnapshot` (extended)

```go
type RegistrySnapshot struct {
    // existing fields unchanged
    Subscriptions []Subscription
    RouteGroups   []RouteGroup

    // NEW (PR 7)
    GRPCServices []GRPCServiceSpec
}
```

---

## `runtime/grpc`

### `ServiceRegistrar` (new)

**As-built (PR-7 #1150): single public path + unexported interceptor.** The public
`ServiceRegistrar` exposes only `Register(spec)` + `CellIDForMethod` — it does **not** implement
`grpc.ServiceRegistrar` (no raw `RegisterService` bypass). Attribution is captured by an unexported
`cellScopedRegistrar` that *does* implement `grpc.ServiceRegistrar`: `Register(spec)` hands it to
the spec's callback, and it intercepts the callback's `RegisterService(sd, impl)` to record
`/{ServiceName}/{method} → cellID` (Methods + Streams) before delegating to the real server.

```go
type ServiceRegistrar struct {
    inner   grpc.ServiceRegistrar // typically *grpc.Server; NOT re-exported
    methods map[string]string     // "/svc/method" → cellID (for PR-9 attribution)
    names   map[string]struct{}   // ServiceName dedup across specs
}

// As-built: two-phase, no inner arg. interceptor.NewServerInterceptors is the sole
// minter (#1752); BindServer sets the delegation target after grpc.NewServer.
func NewServiceRegistrar() *ServiceRegistrar
func (r *ServiceRegistrar) BindServer(inner grpc.ServiceRegistrar)
func (r *ServiceRegistrar) Register(spec cell.GRPCServiceSpec) error
func (r *ServiceRegistrar) CellIDForMethod(fullMethod string) (cellID string, ok bool)
```

**Invariants**:
- `Register` MUST be called before `grpc.Server.Serve()` (drain runs in bootstrap phase7b before `grpcServeAll`).
- Duplicate `ContractID` → fail-fast error at the recorder (`RegistryRecorder.GRPCService`).
- `spec.Register.(func(grpc.ServiceRegistrar))` assertion MUST succeed; on failure, panic via `panicregister.Approved("grpc-registrar-bad-register-fn", ...)`.
- Duplicate `ServiceName` across specs → panic via `panicregister.Approved("grpc-registrar-dup-service", ...)` before grpc-go's own fatal.
- `adapters/grpc.Server.Registrar()` returns the `cell.GRPCServiceRegistrar` interface (the concrete `*ServiceRegistrar`, carrying `CellIDForMethod`, is held inside the adapter for PR-9). `WithGRPCListener` gained a leading `ref cell.ListenerRef`.

---

## `runtime/grpc/interceptor`

### `Chain` (new)

```go
// Chain composes the GoCell-required unary interceptors in canonical order:
//   Recovery (outermost) → Tracing → Metrics → AccessLog → RequestID → Auth →
//   RateLimit → CircuitBreaker → CellAttribution → (handler)
type Chain struct {
    Recovery       grpc.UnaryServerInterceptor
    Tracing        grpc.UnaryServerInterceptor
    Metrics        grpc.UnaryServerInterceptor
    AccessLog      grpc.UnaryServerInterceptor
    RequestID      grpc.UnaryServerInterceptor
    Auth           grpc.UnaryServerInterceptor
    RateLimit      grpc.UnaryServerInterceptor
    CircuitBreaker grpc.UnaryServerInterceptor
    CellAttribution grpc.UnaryServerInterceptor
}

func (c Chain) Build() grpc.ServerOption
```

Stream chain mirrors but uses `grpc.StreamServerInterceptor`.

### Interceptor signatures

```go
type Recovery struct { Logger *slog.Logger }
type Tracing  struct { Tracer trace.Tracer }
type Metrics  struct { Meter metric.Meter }
// ... and so on, each with a `func (x X) Unary() grpc.UnaryServerInterceptor` factory
```

---

## `adapters/grpc`

### `Server` (new)

```go
type Server struct {
    server   *grpc.Server
    config   ServerConfig
    listener net.Listener
}

type ServerConfig struct {
    Addr           string
    TLS            *tls.Config       // nil → plaintext, gated by explicit AllowInsecure opt-in (fail-closed: zero-value rejected), not restricted to loopback — mesh-sidecar plaintext is a valid production posture
    MaxRecvMsgSize int               // default 4 MiB; matches grpc-go default
    KeepaliveParams keepalive.ServerParameters
}

func NewServer(cfg ServerConfig, opts ...ServerOption) (*Server, error)
func (s *Server) Serve(ctx context.Context) error
func (s *Server) GracefulStop(ctx context.Context) error
func (s *Server) Registrar() *runtimegrpc.ServiceRegistrar
```

### `ProbeReady` (new)

```go
const ProbeReady healthz.ReadyProbeName = "grpc_ready"
```

Reports listener bound + accepting status; integrated into `/readyz` four-channel redaction.

---

## `tools/codegen/contractgen`

### `ProtoRegistry` (new)

In-memory map populated during a generate run, keyed by `(ProtoPackage, Service)`. Detects collisions across all contracts and resolves the canonical import path for the generated stub.

```go
type ProtoRegistry struct {
    entries map[protoServiceKey]protoServiceEntry
}

type protoServiceKey struct { Package, Service string }
type protoServiceEntry struct {
    ProtoFile string
    Methods   map[string]MethodMetadata
    Cells     []string  // populated from contractUsages — must be exactly 1 (server)
}
```

### Templates (new)

- `grpc_server.tmpl` — emits server handler interface + `RegisterXxxServer(...)` wrapper that the cellgen call references
- `grpc_client.tmpl` — emits typed client invoker per operation

Generated outputs live in `generated/contracts/grpc/{domain}/{version}/`:
- `server_gen.go` — server handler interface
- `client_gen.go` — typed client invoker
- `methods_gen.go` — method registry (consumed by attribution interceptor)
- `<service>.pb.go` — proto messages (buf product, regenerated alongside)

All four are `// Code generated by ...; DO NOT EDIT.`; `PROTO-GEN-NO-HANDWRITE-01` archtest enforces the header.

---

## State transitions

None. Transport-only feature; no stateful entities.

---

## Validation rules summary

| Rule | Source | PR |
|------|--------|----|
| `kind=grpc` requires `grpc.service` + `grpc.proto` (service-level per ADR D5 / #1655; `grpc.method` deleted) | metadata parser | PR 1 |
| `grpc.proto` MUST resolve under `contracts/grpc/` | metadata parser | PR 1 |
| `(package, service)` globally unique | ProtoRegistry at codegen time | PR 6 |
| Handler signature MUST match generated interface | Go compiler (interface assertion) | PR 7 |
| Service registration MUST happen before `Serve()` | `ServiceRegistrar.Register` ordering | PR 7 |
| `func(grpc.ServiceRegistrar)` callback (Form B) type assertion safety | runtime/grpc panics with Approved marker on bad/typed-nil callback type | PR 7 |
| Hand-written grpc **service** registration banned in `cells/` | archtest `GRPC-SERVICE-IN-CONTRACT-01` (service-level per ADR D5 / #1655) | PR 8 |
| Exhaustive `errcode.Kind → codes.Code` mapping | archtest `GRPC-ERRCODE-MAPPING-01` | PR 12 |
