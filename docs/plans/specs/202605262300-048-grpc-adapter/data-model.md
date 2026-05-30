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
    Method        string            // proto method name, e.g. "IssueCommand"
    StreamingType StreamingType     // unary | server-stream | client-stream | bidi
    ProtoFile     string            // contracts-relative path, e.g. "contracts/grpc/device/command/v1/device_command.proto"
    ProtoPackage  string            // proto file's `package` declaration, e.g. "device.command.v1"
    Auth          GRPCAuthSpec
}

type StreamingType string

const (
    StreamingUnary        StreamingType = "unary"
    StreamingServerStream StreamingType = "server-stream"
    StreamingClientStream StreamingType = "client-stream"
    StreamingBidi         StreamingType = "bidi"
)

type GRPCAuthSpec struct {
    Public bool  // mirrors http.auth.public
}
```

**Invariants**:
- `Service` MUST be non-empty when `Kind == KindGRPC` (validated in PR 1).
- `Method` MUST be non-empty when `Kind == KindGRPC`.
- `ProtoFile` MUST resolve to a file inside `contracts/grpc/`; absolute paths forbidden.
- `(ProtoPackage, Service, Method)` triple MUST be globally unique across all contracts in the repo (proto registry collision check at codegen time).
- `StreamingType` defaults to `unary` if omitted in YAML (PR 1 parser default).

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
    Service       string
    Method        string
    StreamingType StreamingType
    Proto         string  // relative path, validated against contracts/grpc/ tree
    Auth          struct {
        Public bool
    }
}
```

YAML shape (PR 1):

```yaml
id: grpc.device.command.v1.IssueCommand
kind: grpc
grpc:
  service: device.command.v1.DeviceCommandService
  method: IssueCommand
  streamingType: unary       # optional, defaults to unary
  proto: contracts/grpc/device/command/v1/device_command.proto
  auth:
    public: false            # internal-only by default
```

---

## `kernel/cell`

### `GRPCServiceSpec` (new)

```go
// GRPCServiceSpec carries the registration intent for a gRPC service belonging
// to a cell. It deliberately stores the grpc.ServiceDesc as `any` so that
// kernel/ does NOT import google.golang.org/grpc.
//
// Hard upgrade tracked: replace `any` with a sealed marker interface defined
// in adapters/grpc with a private constructor — see plan.md Complexity Tracking.
type GRPCServiceSpec struct {
    ContractID  string
    CellID      string
    ServiceDesc any  // expected *grpc.ServiceDesc, asserted in adapter layer
    Impl        any  // expected the service interface impl, asserted in adapter layer
}
```

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

```go
// ServiceRegistrar is the bootstrap-side drain target for kernel/cell
// GRPCServices. It owns the grpc.Server and performs the actual
// RegisterService call with proper type assertions.
type ServiceRegistrar struct {
    server   *grpc.Server                              // adapters/grpc value
    methods  map[string]string                         // "/svc/method" → cellID, used by attribution interceptor
}

func NewServiceRegistrar(server *grpc.Server) *ServiceRegistrar

func (r *ServiceRegistrar) Register(spec cell.GRPCServiceSpec) error
func (r *ServiceRegistrar) CellIDForMethod(fullMethod string) (cellID string, ok bool)
```

**Invariants**:
- `Register` MUST be called before `grpc.Server.Serve()`.
- Duplicate `ContractID` → fail-fast.
- `(spec.ServiceDesc).(*grpc.ServiceDesc)` assertion MUST succeed; on failure, panic via `panicregister.Approved("grpc-registrar-bad-servicedesc", ...)`.

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
| `kind=grpc` requires `grpc.service` + `grpc.method` | metadata parser | PR 1 |
| `grpc.proto` MUST resolve under `contracts/grpc/` | metadata parser | PR 1 |
| `(package, service, method)` globally unique | ProtoRegistry at codegen time | PR 6 |
| Handler signature MUST match generated interface | Go compiler (interface assertion) | PR 7 |
| Service registration MUST happen before `Serve()` | `ServiceRegistrar.Register` ordering | PR 7 |
| `*grpc.ServiceDesc` type assertion safety | adapter layer panic with Approved marker | PR 7 |
| Hand-written grpc method registration banned in `cells/` | archtest `GRPC-METHOD-IN-CONTRACT-01` | PR 8 |
| Exhaustive `errcode.Kind → codes.Code` mapping | archtest `GRPC-ERRCODE-MAPPING-01` | PR 12 |
