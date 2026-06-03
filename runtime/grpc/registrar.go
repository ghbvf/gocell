package grpc

// registrar.go — Cell-facing gRPC service registration seam (GAP-1 PR-7 [#1150]).
//
// This is the single public registration path for gRPC services in GoCell.
// The key design decisions:
//
//  1. ServiceRegistrar does NOT implement grpc.ServiceRegistrar — there is only ONE
//     public registration entry (Register(spec)), preventing raw RegisterService calls
//     that would bypass attribution.
//
//  2. An unexported cellScopedRegistrar implements grpc.ServiceRegistrar. It is handed
//     to the spec.Register callback, intercepts RegisterService(sd,impl) to record
//     /{ServiceName}/{method}→cellID for every sd.Methods and sd.Streams, then
//     delegates to the real server.
//
//  3. ServiceName deduplication is across ALL specs (shared names map). A second
//     spec whose callback registers the same service name panics before grpc-go's own
//     fatal (which gives a less helpful message about cross-cell collision).
//
//  4. The attribution map built here (method→cellID) is the foundation for PR-9
//     (gRPC metrics cell label) and PR-10 (streaming interceptors). PR-7 does not
//     wire it into any interceptor — it only exposes CellIDForMethod.
//
// ref: zeromicro/go-zero zrpc/internal/rpcserver.go — RegisterFn (Form B precedent)
// ref: go-kratos/kratos transport/grpc/server.go — pb.RegisterXxxServer before Start
// ref: grpc/grpc-go server.go — RegisterService fatals after Serve + dup ServiceName

import (
	"fmt"
	"sync"

	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// ServiceRegistrar is the cell-facing gRPC service registration seam.
// It is constructed by adapters/grpc.Server.Registrar() and handed to bootstrap
// which calls Register(spec) for each GRPCServiceSpec drained from the cell
// snapshot. The grpc.ServiceRegistrar interface is NOT exported — the only entry
// point is Register(spec), which routes through the attribution-aware
// cellScopedRegistrar interceptor.
//
// All public methods are safe for concurrent use after construction.
type ServiceRegistrar struct {
	inner grpc.ServiceRegistrar
	mu    sync.RWMutex
	// methods maps /{ServiceName}/{methodName} → cellID.
	methods map[string]string
	// names tracks registered service names for dedup (shared with cellScopedRegistrar).
	names map[string]struct{}
}

// NewServiceRegistrar wraps inner (typically *grpc.Server) into a ServiceRegistrar.
// inner must not be nil (caller's responsibility; adapters/grpc.New guarantees this).
func NewServiceRegistrar(inner grpc.ServiceRegistrar) *ServiceRegistrar {
	return &ServiceRegistrar{
		inner:   inner,
		methods: make(map[string]string),
		names:   make(map[string]struct{}),
	}
}

// Register invokes the spec.Register callback via an attribution-aware
// cellScopedRegistrar interceptor, then records every method/stream exposed by
// the registered service under spec.CellID.
//
// spec.Register must be a func(grpc.ServiceRegistrar). Any other dynamic type
// panics with panicregister.Approved("grpc-registrar-bad-register-fn", …).
//
// If the callback attempts to register a service whose ServiceName is already
// registered (cross-cell collision), cellScopedRegistrar panics with
// panicregister.Approved("grpc-registrar-dup-service", …) before grpc-go's own
// fatal — providing a more informative message.
//
// Register must be called before grpcServer.Serve (enforced by the drain ordering:
// bootstrap calls Register in phase7b before grpcServeAll).
func (r *ServiceRegistrar) Register(spec cell.GRPCServiceSpec) error {
	// Type-assert: kernel stores Register as `any` to stay grpc-import-free.
	fn, ok := spec.Register.(func(grpc.ServiceRegistrar))
	if !ok {
		panic(panicregister.Approved("grpc-registrar-bad-register-fn",
			errcode.Assertion(
				"grpc: GRPCServiceSpec.Register must be func(grpc.ServiceRegistrar); "+
					"got %T (contractID=%q, cellID=%q)",
				spec.Register, spec.ContractID, spec.CellID)))
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	scoped := &cellScopedRegistrar{
		inner:   r.inner,
		cellID:  spec.CellID,
		methods: r.methods,
		names:   r.names,
	}
	fn(scoped)
	return nil
}

// CellIDForMethod returns the cellID attributed to fullMethod (e.g.
// "/grpc.health.v1.Health/Check"). The second return value is false when the
// method has not been registered via Register. Safe for concurrent use.
func (r *ServiceRegistrar) CellIDForMethod(fullMethod string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.methods[fullMethod]
	return id, ok
}

// ---------------------------------------------------------------------------
// cellScopedRegistrar — unexported attribution interceptor
// ---------------------------------------------------------------------------

// cellScopedRegistrar implements grpc.ServiceRegistrar. It is the value handed
// to each spec.Register callback. It records every /{ServiceName}/{method} →
// cellID entry into the shared methods map, deduplicates service names across
// specs, and delegates to the real server.
//
// It shares the methods and names maps with its parent ServiceRegistrar, so
// attribution data is immediately visible via CellIDForMethod once Register
// returns.
type cellScopedRegistrar struct {
	inner   grpc.ServiceRegistrar
	cellID  string
	methods map[string]string   // shared with ServiceRegistrar
	names   map[string]struct{} // shared with ServiceRegistrar
}

// RegisterService implements grpc.ServiceRegistrar. It:
//
//  1. Deduplicates by sd.ServiceName — panics on collision (cross-cell bug).
//  2. Records /{ServiceName}/{method} → cellID for every Methods + Streams entry.
//  3. Delegates to r.inner.RegisterService(sd, impl) so grpc-go actually registers
//     the service (which fatals if called after Serve — the drain ordering prevents this).
func (c *cellScopedRegistrar) RegisterService(sd *grpc.ServiceDesc, impl any) {
	svcName := sd.ServiceName
	if _, dup := c.names[svcName]; dup {
		panic(panicregister.Approved("grpc-registrar-dup-service",
			errcode.Assertion(
				"grpc: duplicate ServiceName %q: this service is already registered "+
					"(cross-cell collision or duplicate spec.Register callback). "+
					"Each gRPC service must be registered exactly once.",
				svcName)))
	}
	c.names[svcName] = struct{}{}

	// Record every unary method.
	for _, m := range sd.Methods {
		key := fmt.Sprintf("/%s/%s", svcName, m.MethodName)
		c.methods[key] = c.cellID
	}
	// Record every streaming method.
	for _, s := range sd.Streams {
		key := fmt.Sprintf("/%s/%s", svcName, s.StreamName)
		c.methods[key] = c.cellID
	}

	c.inner.RegisterService(sd, impl)
}
