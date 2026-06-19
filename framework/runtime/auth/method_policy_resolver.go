package auth

import (
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// staticMethodPolicyResolver is an immutable, map-backed authz.MethodPolicyResolver.
// It is the HTTP transport's concretion of the transport-neutral resolver seam —
// the sibling of *runtime/grpc.ServiceRegistrar, which satisfies the same interface
// for the gRPC transport (#2205).
type staticMethodPolicyResolver struct {
	byKey map[string]authz.Permission
}

// Compile-time assertion that the concrete static resolver satisfies the
// transport-neutral seam (the HTTP-side sibling of registrar.go's
// `var _ authz.MethodPolicyResolver = (*ServiceRegistrar)(nil)`).
var _ authz.MethodPolicyResolver = staticMethodPolicyResolver{}

// PermissionForMethod resolves a contract id to its sealed Permission. Unmapped
// keys fail closed (ok=false) per the MethodPolicyResolver contract.
func (r staticMethodPolicyResolver) PermissionForMethod(methodKey string) (authz.Permission, bool) {
	p, ok := r.byKey[methodKey]
	return p, ok
}

// NewStaticMethodPolicyResolver builds a cell-level authz.MethodPolicyResolver from
// a contractID→action-string map — the HTTP sibling of the gRPC registrar's
// recordMethodOverlays map. cellgen derives the map from each served HTTP contract's
// endpoints.http.permission overlay; this constructor resolves every action string
// through the closed authz registry (authz.PermissionByName), so the resolver only
// ever hands back sealed, pre-registered Permission values (the seal is preserved —
// no Permission is minted here).
//
// An action string outside the registry is codegen/registry drift: governance
// validates membership at `gocell validate` (the FMT HTTP-permission rule, the
// sibling of FMT-41), so reaching here with an unknown action means that gate was
// bypassed or the registry changed under generated code → fail-fast panic, mirroring
// registrar.recordMethodOverlays' grpc-registrar-unknown-permission. The returned
// resolver is immutable.
func NewStaticMethodPolicyResolver(byKeyAction map[string]string) authz.MethodPolicyResolver {
	byKey := make(map[string]authz.Permission, len(byKeyAction))
	for key, action := range byKeyAction {
		p, ok := authz.PermissionByName(action)
		if !ok {
			panic(panicregister.Approved("authz-unknown-method-permission",
				errcode.Assertion("NewStaticMethodPolicyResolver: action %q for key %q is not a registered "+
					"authz.Permission (codegen/registry drift; regenerate via gocell generate cell or "+
					"reconcile the authz registry)", action, key)))
		}
		byKey[key] = p
	}
	return staticMethodPolicyResolver{byKey: byKey}
}

// RequirePermissionForContract returns the contract-derived HTTP route gate. It is the
// SINGLE funnel for all three HTTP PDP gate shapes (#2355): it resolves the contract's
// permission through the cell-level authz.MethodPolicyResolver ONCE at construction
// (Mount/Init time), then dispatches on the gate shape carried by the ContractSpec —
//
//   - spec.Resource != "" → RequirePermissionForResource(spec.Resource, perm): owner-scoped,
//     forwards the named path param's value to the PDP as the ownership resource.
//   - spec.SelfScoped      → RequirePermissionForSelf(perm): self-scoped, forwards the
//     caller's own subject (no path param).
//   - neither              → RequirePermission(perm): coarse allow/deny.
//
// All three delegate to enforcePermission — the SOLE PDP route entry — so this adds no
// second PDP path (PR-10a D5 holds). It only changes WHERE the permission and the
// ownership resource come from: contract metadata (resolver + ContractSpec) instead of a
// hand-wired authz.PermX() + RequirePermissionForResource("id", …) literal in slice code.
//
// It is the HTTP sibling of the gRPC interceptor's PermissionResolver + resource-field
// lookup (#2205/#2355): both transports source the route/method permission AND the
// owner-scoped resource shape through contract metadata, unifying the two onto one
// contract-derived origin. Unlike the resolver-carried permission, the resource path
// param and self-scoped flag live on the generated ContractSpec literal (golden-locked,
// alongside Method/Path) — the resolver interface stays transport-neutral (permission-only).
//
// Taking the whole ContractSpec rather than the bare ID is deliberate: the resource/
// self-scoped shape is then read from the single generated truth source, with no second
// helper and no per-route codegen branch (a baked literal in the call would duplicate the
// resource that already lives in the ContractSpec).
//
// A resolver miss is codegen drift — the generated handler carries this gate only when its
// contract declares endpoints.http.permission, and cellgen enrolls every such contract in
// the cell resolver — so it fails fast at construction (mirroring the gRPC registration-time
// fail-fast) rather than surfacing a mystery 403 on first request.
//
// AI-robust Grade: Medium — the gate is callable code, locked to generated callers by
// HTTP-PERMISSION-GATE-WIRING-FUNNEL-01; the contract→permission binding it reads, and the
// resource/selfScoped shape it dispatches on, are golden-locked at codegen (Hard).
func RequirePermissionForContract(spec contractspec.ContractSpec, resolver authz.MethodPolicyResolver) Policy {
	// Exported-helper self-defense: the generated handler is the only sanctioned
	// caller (HTTP-PERMISSION-GATE-WIRING-FUNNEL-01), but this func is exported, so it
	// guards its own resolver contract here rather than relying on the caller. A nil
	// (or typed-nil) resolver is a wiring bug — fail fast via panicregister, the
	// package convention (NewStaticMethodPolicyResolver does the same), so it never
	// becomes a bare Go nil-deref panic. validation.IsNilInterface catches typed-nil a
	// bare == nil would miss.
	if validation.IsNilInterface(resolver) {
		panic(panicregister.Approved("http-permission-nil-resolver",
			errcode.Assertion("RequirePermissionForContract: resolver must not be nil for contract %q (the cell "+
				"authz.MethodPolicyResolver must be injected by cellgen-wired NewHandler; a nil/typed-nil resolver "+
				"is a wiring bug)", spec.ID)))
	}
	perm, ok := resolver.PermissionForMethod(spec.ID)
	if !ok {
		panic(panicregister.Approved("http-permission-unmapped",
			errcode.Assertion("RequirePermissionForContract: contract %q has no permission mapping in the cell "+
				"MethodPolicyResolver (codegen drift — the contract must declare endpoints.http.permission and "+
				"cellgen must enroll it; regenerate via gocell generate cell)", spec.ID)))
	}
	switch {
	case spec.Resource != "":
		return RequirePermissionForResource(spec.Resource, perm)
	case spec.SelfScoped:
		return RequirePermissionForSelf(perm)
	default:
		return RequirePermission(perm)
	}
}
