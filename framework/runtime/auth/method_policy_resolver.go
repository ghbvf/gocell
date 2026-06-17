package auth

import (
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
)

// staticMethodPolicyResolver is an immutable, map-backed authz.MethodPolicyResolver.
// It is the HTTP transport's concretion of the transport-neutral resolver seam —
// the sibling of *runtime/grpc.ServiceRegistrar, which satisfies the same interface
// for the gRPC transport (#2205).
type staticMethodPolicyResolver struct {
	byKey map[string]authz.Permission
}

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
				errcode.Assertion("NewStaticMethodPolicyResolver: action %q for key %q is not a registered authz.Permission (codegen/registry drift; regenerate via gocell generate cell or reconcile the authz registry)", action, key)))
		}
		byKey[key] = p
	}
	return staticMethodPolicyResolver{byKey: byKey}
}

// RequirePermissionByName returns the contract-derived HTTP route gate: it resolves
// contractID through the cell-level authz.MethodPolicyResolver ONCE at construction
// (Mount/Init time) and delegates to RequirePermission(perm) — the SOLE PDP route
// entry, so this adds no second PDP path (PR-10a D5 holds). It only changes WHERE the
// permission comes from: a contract-derived resolver instead of a hand-wired
// authz.PermX() literal in slice code.
//
// It is the HTTP sibling of the gRPC interceptor's PermissionResolver lookup (#2205):
// both transports source the route/method permission through an
// authz.MethodPolicyResolver backed by contract metadata, unifying the two onto one
// contract-derived origin.
//
// A resolver miss is codegen drift — the generated handler carries this gate only
// when its contract declares endpoints.http.permission, and cellgen enrolls every
// such contract in the cell resolver — so it fails fast at construction (mirroring
// the gRPC registration-time fail-fast) rather than surfacing a mystery 403 on first
// request.
//
// AI-robust Grade: Medium — the gate is callable code, locked to generated callers by
// HTTP-PERMISSION-GATE-WIRING-FUNNEL-01; the contract→permission binding it reads is
// golden-locked at codegen (Hard).
func RequirePermissionByName(contractID string, resolver authz.MethodPolicyResolver) Policy {
	perm, ok := resolver.PermissionForMethod(contractID)
	if !ok {
		panic(panicregister.Approved("http-permission-unmapped",
			errcode.Assertion("RequirePermissionByName: contract %q has no permission mapping in the cell MethodPolicyResolver (codegen drift — the contract must declare endpoints.http.permission and cellgen must enroll it; regenerate via gocell generate cell)", contractID)))
	}
	return RequirePermission(perm)
}
