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
//     CellIDForMethod feeds interceptor.UnaryCellAttribution, which writes
//     ctxkeys.CellID so the gRPC metrics cell label and access log reflect the
//     owning cell. Streaming interceptors (PR-10) reuse the same map. The registrar
//     this reads is the one interceptor.NewServerInterceptors mints and the adapter
//     binds — one instance for both attribution and registration (#1752).
//
// ref: zeromicro/go-zero zrpc/internal/rpcserver.go — RegisterFn (Form B precedent)
// ref: go-kratos/kratos transport/grpc/server.go — pb.RegisterXxxServer before Start
// ref: grpc/grpc-go server.go — RegisterService fatals after Serve + dup ServiceName

import (
	"fmt"
	"sync"

	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
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
	// publicMethods is the set of FULL method names (/{ServiceName}/{method})
	// declared JWT-exempt by a spec's GRPCServiceSpec.PublicMethods (#1675). It is
	// the single runtime source the auth interceptor consults via IsPublicMethod;
	// an absent method is authed (fail-closed). Populated during Register, like
	// the methods attribution map.
	publicMethods map[string]struct{}
	// methodPermissions maps each non-public FULL method name to the sealed
	// authz.Permission it requires (#2008), resolved from
	// GRPCServiceSpec.MethodPermissions at Register time (a string that is not a
	// member of the closed authz registry fails fast there). It is the single
	// runtime source the auth interceptor consults via PermissionForMethod for the
	// PDP gate — the authorization sibling of publicMethods (authentication bypass).
	// An absent method has no mapping → the gate DENIES (strict fail-closed).
	methodPermissions map[string]authz.Permission
	// names maps a registered gRPC ServiceName → its owning spec, used both for
	// cross-spec dedup and to report first/current owner on a collision (shared
	// with cellScopedRegistrar).
	names map[string]serviceOwner
	// permissionGateWired records whether the auth interceptor chain that mints
	// this registrar was supplied a PDP Authorizer (#2008, F1 startup parity).
	// NewServerInterceptors threads !IsNilInterface(deps.Authorizer) here via
	// WithPermissionGate. Register fail-fasts when a spec declares
	// permission-gated methods (non-empty MethodPermissions) but this is false —
	// the gRPC analog of HTTP's bootstrap.ResolveAuthorizer startup guard: a
	// permission-gated method with no Authorizer would otherwise boot, pass
	// grpc_ready, and DENY every protected RPC at request time. The phase7b drain
	// (Init done, pre-Serve) surfaces the wiring bug at startup instead.
	permissionGateWired bool
}

// RegistrarOption configures a ServiceRegistrar at construction. The only option
// today is WithPermissionGate; the variadic form keeps NewServiceRegistrar's
// existing zero-arg call sites (tests minting a bare registrar) compiling while
// letting NewServerInterceptors declare the PDP-gate wiring state.
type RegistrarOption func(*ServiceRegistrar)

// WithPermissionGate declares whether the auth interceptor that mints this
// registrar was wired a PDP Authorizer (#2008, F1). interceptor.NewServerInterceptors
// passes !validation.IsNilInterface(deps.Authorizer); Register then refuses to
// register a spec with permission-gated methods when no Authorizer backs the gate
// (startup fail-fast, mirroring HTTP's ResolveAuthorizer pre-serve guard).
func WithPermissionGate(wired bool) RegistrarOption {
	return func(r *ServiceRegistrar) { r.permissionGateWired = wired }
}

// serviceOwner records which spec first registered a given gRPC ServiceName, so
// a later duplicate registration can name both sides of the collision.
type serviceOwner struct {
	cellID     string
	contractID string
}

// NewServiceRegistrar builds a ServiceRegistrar with an empty attribution map and
// no delegation target (two-phase, Option 3 #1152). interceptor.NewServerInterceptors
// is the SOLE production caller (#1752, GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01): it
// mints the registrar, hands reg.CellIDForMethod to the unary + stream chains —
// composed BEFORE the gRPC server exists — and carries the same instance in the
// bundle so the adapter binds the delegation target via BindServer once
// grpc.NewServer has been constructed with that chain. CellIDForMethod is callable
// immediately (the map exists from construction); it returns matches once Register
// has populated it during the bootstrap drain.
func NewServiceRegistrar(opts ...RegistrarOption) *ServiceRegistrar {
	r := &ServiceRegistrar{
		methods:           make(map[string]string),
		publicMethods:     make(map[string]struct{}),
		methodPermissions: make(map[string]authz.Permission),
		names:             make(map[string]serviceOwner),
	}
	for _, o := range opts {
		o(r)
	}
	return r
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
		inner:        r.inner,
		cellID:       spec.CellID,
		contractID:   spec.ContractID,
		methods:      r.methods,
		names:        r.names,
		localMethods: make(map[string]struct{}),
		active:       true,
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

	// Startup parity guard (#2008, F1): a spec with permission-gated methods needs
	// a PDP Authorizer behind the gate. Without one the gate denies every such RPC
	// at request time (fail-closed, but surfaced at first call rather than boot) —
	// the HTTP path fails fast at bootstrap.ResolveAuthorizer before serving. This
	// drain runs in phase7b (after Init, before Serve), so the panic surfaces the
	// missing composition-root wiring (interceptor.Deps.Authorizer, threaded here
	// via WithPermissionGate) at startup, achieving HTTP/gRPC parity.
	if len(spec.MethodPermissions) > 0 && !r.permissionGateWired {
		panic(panicregister.Approved("grpc-registrar-permission-gate-unwired",
			errcode.Assertion(
				"grpc: GRPCServiceSpec.MethodPermissions declares %d permission-gated method(s) "+
					"(contractID=%q, cellID=%q) but no PDP Authorizer is wired into the gRPC auth "+
					"interceptor; set interceptor.Deps.Authorizer in the composition root (the gRPC "+
					"analog of bootstrap.WithPrimaryAuthorizer)",
				len(spec.MethodPermissions), spec.ContractID, spec.CellID)))
	}

	// Record the per-method public-auth overlay (#1675): spec.PublicMethods
	// (cellgen-derived from endpoints.grpc.methods[] public:true entries, keyed
	// identically to the attribution map's /{ServiceName}/{method}) become the
	// auth interceptor's bypass set via IsPublicMethod. Referential integrity —
	// each entry ∈ this spec's registered method set — is enforced at build time
	// (contractgen pre-pass + cellgen golden + governance FMT-41) AND re-checked
	// here at runtime (#2008, F2 defense-in-depth: a hand-written spec bypassing
	// codegen, or a stale key, fails fast rather than carrying an inert entry).
	// Recorded under the same write lock as the attribution map.
	for _, m := range spec.PublicMethods {
		if _, ok := scoped.localMethods[m]; !ok {
			panic(panicregister.Approved("grpc-registrar-unknown-method-key",
				errcode.Assertion(
					"grpc: GRPCServiceSpec.PublicMethods[%q] does not name a method registered by this "+
						"spec (contractID=%q, cellID=%q); the public-method overlay must reference a real "+
						"RPC — declare it via endpoints.grpc.methods[].public",
					m, spec.ContractID, spec.CellID)))
		}
		r.publicMethods[m] = struct{}{}
	}

	// Record the per-method ABAC permission overlay (#2008): spec.MethodPermissions
	// (cellgen-derived from endpoints.grpc.methods[] permission entries, keyed
	// identically to the attribution map) become the auth interceptor's PDP gate
	// source via PermissionForMethod. Two fail-fast checks (both wiring bugs only
	// reachable by a hand-written spec bypassing codegen, since contractgen +
	// FMT-41 already guard the build): the full-method KEY must name a method this
	// spec registered (F2 referential integrity — a stale key would DENY a
	// non-existent RPC, a dead 403), and the permission VALUE must be a member of
	// the closed authz registry. Recorded under the same write lock as the
	// attribution map.
	for method, permName := range spec.MethodPermissions {
		if _, ok := scoped.localMethods[method]; !ok {
			panic(panicregister.Approved("grpc-registrar-unknown-method-key",
				errcode.Assertion(
					"grpc: GRPCServiceSpec.MethodPermissions[%q] does not name a method registered by "+
						"this spec (contractID=%q, cellID=%q); the permission overlay must reference a real "+
						"RPC — declare it via endpoints.grpc.methods[].permission",
					method, spec.ContractID, spec.CellID)))
		}
		perm, ok := authz.PermissionByName(permName)
		if !ok {
			panic(panicregister.Approved("grpc-registrar-unknown-permission",
				errcode.Assertion(
					"grpc: GRPCServiceSpec.MethodPermissions[%q]=%q is not a known authz.Permission "+
						"(contractID=%q, cellID=%q); the per-method permission overlay must reference the "+
						"closed authz registry — declare it via endpoints.grpc.methods[].permission",
					method, permName, spec.ContractID, spec.CellID)))
		}
		r.methodPermissions[method] = perm
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

// IsPublicMethod reports whether fullMethod (e.g. "/grpc.health.v1.Health/Check")
// was declared JWT-exempt via a spec's GRPCServiceSpec.PublicMethods (#1675). The
// fail-closed default is false: an unknown or undeclared method is authed. Safe
// for concurrent use. The auth interceptor installs this as its WithPublicMethod
// predicate (chain.go / stream.go), making the registrar the single runtime
// source of the public-method set — mirroring how CellIDForMethod sources cell
// attribution.
func (r *ServiceRegistrar) IsPublicMethod(fullMethod string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.publicMethods[fullMethod]
	return ok
}

// PermissionForMethod returns the sealed authz.Permission a non-public RPC
// requires (#2008), resolved from the cell's endpoints.grpc.methods[].permission
// overlay at Register time. The second return value is false when fullMethod has
// no permission mapping — the fail-closed default the auth interceptor's PDP gate
// treats as DENY (a non-public RPC with no declared permission is a dead method,
// not an authn-only one). Safe for concurrent use. The auth interceptor installs
// this as its WithPermissionResolver, making the registrar the single runtime
// source of the method→permission map — the authorization sibling of
// IsPublicMethod (authentication bypass).
func (r *ServiceRegistrar) PermissionForMethod(fullMethod string) (authz.Permission, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.methodPermissions[fullMethod]
	return p, ok
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
	inner        grpc.ServiceRegistrar
	cellID       string
	contractID   string
	methods      map[string]string       // shared with ServiceRegistrar
	names        map[string]serviceOwner // shared with ServiceRegistrar
	localMethods map[string]struct{}     // full-method keys registered by THIS spec (F2 referential check)
	active       bool                    // true only during the spec.Register callback
	count        int                     // number of RegisterService calls in this scope
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

	// Record every unary method (shared attribution map + this spec's local set).
	for _, m := range sd.Methods {
		key := fmt.Sprintf("/%s/%s", svcName, m.MethodName)
		c.methods[key] = c.cellID
		c.localMethods[key] = struct{}{}
	}
	// Record every streaming method.
	for _, s := range sd.Streams {
		key := fmt.Sprintf("/%s/%s", svcName, s.StreamName)
		c.methods[key] = c.cellID
		c.localMethods[key] = struct{}{}
	}

	c.inner.RegisterService(sd, impl)
}
