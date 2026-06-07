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
//  4. The attribution map built here (method→cellID) is consumed by PR-9 (#1152):
//     CellIDForMethod feeds interceptor.UnaryCellAttribution (via Deps.Registrar),
//     which writes ctxkeys.CellID so the gRPC metrics cell label and access log
//     reflect the owning cell. Streaming interceptors (PR-10) reuse the same map.
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
	"github.com/ghbvf/gocell/pkg/validation"
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
	// names maps a registered gRPC ServiceName → its owning spec, used both for
	// cross-spec dedup and to report first/current owner on a collision (shared
	// with cellScopedRegistrar).
	names map[string]serviceOwner
}

// serviceOwner records which spec first registered a given gRPC ServiceName, so
// a later duplicate registration can name both sides of the collision.
type serviceOwner struct {
	cellID     string
	contractID string
}

// NewServiceRegistrar builds a ServiceRegistrar with an empty attribution map and
// no delegation target (two-phase, Option 3 #1152). The composition root creates
// it FIRST so reg.CellIDForMethod can be handed to the unary interceptor chain —
// which is composed BEFORE the gRPC server exists — and binds the delegation
// target via BindServer once grpc.NewServer has been constructed with that chain.
// CellIDForMethod is callable immediately (the map exists from construction); it
// returns matches once Register has populated it during the bootstrap drain.
func NewServiceRegistrar() *ServiceRegistrar {
	return &ServiceRegistrar{
		methods: make(map[string]string),
		names:   make(map[string]serviceOwner),
	}
}

// BindServer sets the delegation target (typically *grpc.Server) that Register
// forwards RegisterService calls to. It must be called exactly once, during
// adapter construction, before any Register call (the bootstrap drain runs in
// phase7b, after New returns). inner must not be nil and BindServer must not be
// called twice — both are B-class programmer errors raised via
// panicregister.Approved.
func (r *ServiceRegistrar) BindServer(inner grpc.ServiceRegistrar) {
	if validation.IsNilInterface(inner) {
		panic(panicregister.Approved("grpc-registrar-nil-inner",
			errcode.Assertion("BindServer: inner must not be nil")))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inner != nil {
		panic(panicregister.Approved("grpc-registrar-rebind",
			errcode.Assertion("BindServer: delegation target already bound")))
	}
	r.inner = inner
}

// Register invokes the spec.Register callback via an attribution-aware,
// single-use cellScopedRegistrar interceptor, then records every method/stream
// exposed by the registered service under spec.CellID.
//
// This implementation always returns nil; every contract violation panics via
// panicregister.Approved (B-class programmer / config error, fail-fast at
// startup — the drain runs in phase7b before any RPC is served):
//
//   - spec.Register is not a func(grpc.ServiceRegistrar):
//     panicregister.Approved("grpc-registrar-bad-register-fn", …).
//   - spec.Register is a typed-nil callback (a nil func(grpc.ServiceRegistrar)
//     boxed in any — passes kernel's bare-nil Validate but is uninvokable):
//     panicregister.Approved("grpc-registrar-nil-register-fn", …).
//   - the callback does not register exactly one service (zero = silently
//     unserved spec; >1 = a single spec smuggling multiple services):
//     panicregister.Approved("grpc-registrar-service-count", …).
//   - the callback registers a ServiceName already owned by another spec
//     (cross-cell collision): panicregister.Approved("grpc-registrar-dup-service",
//     …) before grpc-go's own fatal — with first/current owner context.
//   - the cellScopedRegistrar is retained and used after the callback returns
//     (escaped scope — would let registration leak past Serve):
//     panicregister.Approved("grpc-registrar-escaped-scope", …).
//
// Register must be called before grpcServer.Serve (enforced by the drain ordering:
// bootstrap calls Register in phase7b before grpcServeAll). The scope is closed
// once the callback returns so a retained registrar cannot register after Serve.
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
	// A typed-nil func passes the assert (ok==true, fn==nil) and slips through
	// kernel's bare-nil GRPCServiceSpec.Validate (which only checks `Register ==
	// nil` on the any). Invoking it would raise an unregistered Go runtime panic;
	// fail-fast through the Approved funnel instead.
	if fn == nil {
		panic(panicregister.Approved("grpc-registrar-nil-register-fn",
			errcode.Assertion(
				"grpc: GRPCServiceSpec.Register is a typed-nil func(grpc.ServiceRegistrar) "+
					"(contractID=%q, cellID=%q); supply a non-nil callback",
				spec.ContractID, spec.CellID)))
	}

	// NOTE: fn(scoped) runs UNDER the write lock. Do NOT call CellIDForMethod
	// inside fn — it takes an RLock and would deadlock. This is safe today
	// because the drain runs serially in phase7b (pre-Serve, single goroutine).
	// Post-Serve registration safety is a PR-9 concern (out of scope here).
	r.mu.Lock()
	defer r.mu.Unlock()

	// Register before BindServer is a wiring bug: the delegation target is
	// unbound, so RegisterService would nil-deref. Fail-fast (the adapter binds
	// the gRPC server in New, before the phase7b drain ever calls Register).
	if r.inner == nil {
		panic(panicregister.Approved("grpc-registrar-unbound",
			errcode.Assertion(
				"grpc: ServiceRegistrar.Register called before BindServer "+
					"(contractID=%q, cellID=%q); bind the gRPC server before draining cell services",
				spec.ContractID, spec.CellID)))
	}

	scoped := &cellScopedRegistrar{
		inner:      r.inner,
		cellID:     spec.CellID,
		contractID: spec.ContractID,
		methods:    r.methods,
		names:      r.names,
		active:     true,
	}
	fn(scoped)
	// Close the scope: a registrar retained by the callback can no longer register
	// (it would otherwise be able to RegisterService after Serve, which grpc-go
	// fatals on). All further RegisterService calls now fail-fast.
	scoped.active = false

	// Each GRPCServiceSpec maps to exactly one gRPC service. Zero means the spec
	// is declared but silently unserved; more than one means a single spec is
	// smuggling multiple services past the contract/attribution model.
	if scoped.count != 1 {
		panic(panicregister.Approved("grpc-registrar-service-count",
			errcode.Assertion(
				"grpc: GRPCServiceSpec.Register callback must register exactly one service "+
					"(contractID=%q, cellID=%q); got %d RegisterService call(s)",
				spec.ContractID, spec.CellID, scoped.count)))
	}
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

// cellScopedRegistrar implements grpc.ServiceRegistrar. It is the single-use
// value handed to one spec.Register callback. It records every
// /{ServiceName}/{method} → cellID entry into the shared methods map,
// deduplicates service names across specs (reporting first/current owner on a
// collision), counts its own RegisterService calls, and delegates to the real
// server.
//
// It shares the methods and names maps with its parent ServiceRegistrar, so
// attribution data is immediately visible via CellIDForMethod once Register
// returns. active gates the registrar to the dynamic extent of the callback:
// Register sets it true before fn(scoped) and false after, so a registrar
// retained past the callback (or used after Serve) fails fast.
type cellScopedRegistrar struct {
	inner      grpc.ServiceRegistrar
	cellID     string
	contractID string
	methods    map[string]string       // shared with ServiceRegistrar
	names      map[string]serviceOwner // shared with ServiceRegistrar
	active     bool                    // true only during the spec.Register callback
	count      int                     // number of RegisterService calls in this scope
}

// RegisterService implements grpc.ServiceRegistrar. It:
//
//  1. Rejects use outside its registration scope (escaped / post-Serve registrar).
//  2. Deduplicates by sd.ServiceName — panics on collision, naming both owners.
//  3. Records /{ServiceName}/{method} → cellID for every Methods + Streams entry.
//  4. Delegates to r.inner.RegisterService(sd, impl) so grpc-go actually registers
//     the service (which fatals if called after Serve — the drain ordering prevents this).
//
// The active/count fields are read and written only under the parent
// ServiceRegistrar write lock held across the whole callback, so the in-extent
// path is race-free; the active guard is a best-effort fail-fast for a registrar
// that escaped its callback (a programmer error that grpc-go's own concurrency
// rules already forbid).
func (c *cellScopedRegistrar) RegisterService(sd *grpc.ServiceDesc, impl any) {
	if !c.active {
		panic(panicregister.Approved("grpc-registrar-escaped-scope",
			errcode.Assertion(
				"grpc: cellScopedRegistrar.RegisterService called outside its registration "+
					"scope (contractID=%q, cellID=%q, service=%q); the registrar must not be "+
					"retained past the spec.Register callback",
				c.contractID, c.cellID, sd.ServiceName)))
	}
	c.count++

	svcName := sd.ServiceName
	if owner, dup := c.names[svcName]; dup {
		panic(panicregister.Approved("grpc-registrar-dup-service",
			errcode.Assertion(
				"grpc: duplicate gRPC ServiceName %q: already registered by cell %q "+
					"(contractID=%q); re-registered by cell %q (contractID=%q). "+
					"Each gRPC service must be registered exactly once across all cells.",
				svcName, owner.cellID, owner.contractID, c.cellID, c.contractID)))
	}
	c.names[svcName] = serviceOwner{cellID: c.cellID, contractID: c.contractID}

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
