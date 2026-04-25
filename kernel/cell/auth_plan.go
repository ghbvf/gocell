package cell

// auth_plan.go — sealed AuthPlan interface + typed plan structs.
//
// Design goals:
//   1. Sealed via unexported marker methods (authPlanKind/listenerAuthOK).
//   2. AuthPlan implementations are valid as the default authentication scheme
//      on a physical HTTP listener (ListenerAuth). Auth scheme is a listener-
//      scope concern — RouteGroups inherit their listener's auth chain. Cells
//      that need a different scheme should declare their routes on a different
//      listener (e.g. webhook listener with HMAC-only chain).
//   3. AuthJWT/AuthJWTFromAssembly install via the router's matcher-aware
//      AuthMiddleware (driven by FinalizeAuth Public/PasswordResetExempt
//      compilation); the chain forces this to the head position via phase0.
//
// Dependency note: kernel/cell MUST NOT import kernel/assembly (cycle via
// assembly.go importing cell). AuthJWTFromAssembly holds an AssemblyIdentity
// interface instead of *assembly.CoreAssembly. Bootstrap (runtime/bootstrap)
// accepts the concrete type and performs the assembly identity check there.
//
// ref: kubernetes/apiserver pkg/authentication/authenticator/interfaces.go — sealed
//      interface + segregated Token/Request/Password authenticators.
// ref: zeromicro/go-zero rest/types.go featuredRoutes — typed plan + single wiring point.

import (
	"fmt"
	"sync/atomic"
)

// MinHMACKeyBytes is the minimum byte length required for HMAC secrets used by
// AuthServiceToken. NIST SP 800-107 §5.3.4 recommends an HMAC key strength of
// at least the security strength of the underlying hash; for HMAC-SHA-256 that
// is 256 bits = 32 bytes. NewAuthServiceToken enforces this at construction
// time; runtime/auth.ServiceTokenMiddleware enforces it again at wiring time
// (defense in depth) so any cell.HMACKeyring implementation that returns a
// shorter Current() secret is rejected at both ends.
const MinHMACKeyBytes = 32

// AuthKind is the discriminant for an AuthPlan variant.
// Declared as uint8 to keep the type small; the iota values are stable.
type AuthKind uint8

const (
	AuthKindNone            AuthKind = iota // AuthNone
	AuthKindJWT                             // AuthJWT
	AuthKindJWTFromAssembly                 // AuthJWTFromAssembly
	AuthKindMTLS                            // AuthMTLS
	AuthKindServiceToken                    // AuthServiceToken
)

// AuthPlan is the sealed base interface for all authentication plans.
// The unexported marker method authPlanKind prevents implementations outside
// this package, giving us a closed enumeration without code generation.
type AuthPlan interface {
	authPlanKind() AuthKind
	// Describe returns a human-readable fingerprint for startup logging.
	// Used by runtime/bootstrap/auth_plan_describe.go (the only allowed location
	// for the "jwt"/"mtls"/"service-token" string literals).
	Describe() string
}

// ListenerAuth is the (only) typed authentication scheme accepted by
// bootstrap.WithListener. RouteGroups inherit their listener's auth chain
// uniformly — there is no group-level override (PR269 round-3: cells that need
// a different scheme should declare their routes on a different listener).
type ListenerAuth interface {
	AuthPlan
	listenerAuthOK() // compile-time seal marker (unexported, prevents external impls)
}

// ─── AuthNone ────────────────────────────────────────────────────────────────

// AuthNone is the no-op auth plan. Use it for listeners that are
// network-isolated and require no authentication gate (e.g. the HealthListener
// behind a Kubernetes probe path).
type AuthNone struct{}

func (AuthNone) authPlanKind() AuthKind { return AuthKindNone }
func (AuthNone) Describe() string       { return "none" }

// listenerAuthOK is the empty seal marker — its mere presence makes AuthNone
// satisfy ListenerAuth at compile time. The unexported method prevents external
// packages from implementing ListenerAuth, closing the enumeration.
func (AuthNone) listenerAuthOK() {}

// Compile-time assertion.
var _ ListenerAuth = AuthNone{}

// ─── AuthJWT ─────────────────────────────────────────────────────────────────

// AuthJWT is the JWT-authenticated listener plan. The verifier is supplied at
// construction time via NewAuthJWT. Bootstrap extracts it during phase5 and
// installs the router-aware AuthMiddleware so that Public/PasswordResetExempt
// routes declared via auth.Mount are honoured.
type AuthJWT struct {
	// Verifier is the IntentTokenVerifier used to validate JWTs. Required; nil
	// is rejected by NewAuthJWT with a panic.
	// Do not set this field directly after construction; use NewAuthJWT(v)
	// which enforces the non-nil invariant.
	Verifier IntentTokenVerifier
}

// NewAuthJWT constructs an AuthJWT plan. Panics if v is nil, following
// bootstrap's fail-fast convention for programmer errors at composition time.
func NewAuthJWT(v IntentTokenVerifier) AuthJWT {
	if v == nil {
		panic("cell: NewAuthJWT verifier must not be nil; use NewAuthJWTFromAssembly(asm) to discover from an authProvider cell")
	}
	return AuthJWT{Verifier: v}
}

func (AuthJWT) authPlanKind() AuthKind { return AuthKindJWT }
func (AuthJWT) Describe() string       { return "jwt" }

// listenerAuthOK seals the ListenerAuth interface (see AuthNone.listenerAuthOK).
func (AuthJWT) listenerAuthOK() {}

// Compile-time assertion.
var _ ListenerAuth = AuthJWT{}

// ─── AuthJWTFromAssembly ──────────────────────────────────────────────────────

// AssemblyRef exposes the minimum contract kernel needs: ID() for identity
// comparison and CellIDs() for authProvider discovery. Using a named interface
// instead of any preserves type safety at composition boundaries.
//
// Bootstrap passes the concrete *assembly.CoreAssembly which satisfies this
// interface structurally; identity checks (same pointer) are done in
// runtime/bootstrap, not in kernel.
type AssemblyRef interface {
	// ID returns the assembly's unique identifier.
	ID() string
	// CellIDs returns the ordered list of registered cell identifiers.
	CellIDs() []string
}

// AuthJWTFromAssembly is a lazy JWT plan that resolves its verifier from an
// AuthProvider cell in the assembly during bootstrap phase4. Use it when the
// verifier is owned by a cell (typical for accesscore-style designs).
//
// The resolved verifier is stored in an atomic.Pointer so that concurrent
// reads from the router are safe without locking.
//
// AuthJWTFromAssembly implements only ListenerAuth (same rationale as AuthJWT).
type AuthJWTFromAssembly struct {
	// Assembly is the AssemblyRef to scan for an AuthProvider cell at phase4.
	// Required; nil is rejected by NewAuthJWTFromAssembly with a panic.
	// In practice this is always *assembly.CoreAssembly from kernel/assembly.
	Assembly AssemblyRef

	// resolved holds the verifier once phase4 has run. Bootstrap writes via
	// SetResolved; subsequent reads by the router are safe across goroutines.
	resolved *atomic.Pointer[IntentTokenVerifier]
}

// NewAuthJWTFromAssembly constructs an AuthJWTFromAssembly plan.
// Panics if asm is nil.
func NewAuthJWTFromAssembly(asm AssemblyRef) AuthJWTFromAssembly {
	if asm == nil {
		panic("cell: NewAuthJWTFromAssembly assembly must not be nil")
	}
	return AuthJWTFromAssembly{
		Assembly: asm,
		resolved: &atomic.Pointer[IntentTokenVerifier]{},
	}
}

func (AuthJWTFromAssembly) authPlanKind() AuthKind { return AuthKindJWTFromAssembly }

// Describe returns "jwt" so that operator dashboards and alert rules that
// filter on auth=jwt match both AuthJWT and AuthJWTFromAssembly paths
// consistently. Both ultimately install the same JWT verifier mechanism.
func (AuthJWTFromAssembly) Describe() string { return "jwt" }

// listenerAuthOK seals the ListenerAuth interface (see AuthNone.listenerAuthOK).
func (AuthJWTFromAssembly) listenerAuthOK() {}

// ResolvedVerifier returns the verifier once it has been discovered by phase4.
// Returns nil before SetResolved has been called.
//
// Method is on value receiver so it works when AuthJWTFromAssembly is stored
// by value in []ListenerAuth. The underlying atomic.Pointer is already a
// pointer so concurrent safety is preserved.
func (p AuthJWTFromAssembly) ResolvedVerifier() IntentTokenVerifier {
	if p.resolved == nil {
		return nil
	}
	vp := p.resolved.Load()
	if vp == nil {
		return nil
	}
	return *vp
}

// SetResolved stores the verifier discovered by phase4. Called by bootstrap;
// must not be called by cell code.
//
// This method is exported to allow access from runtime/bootstrap (a different
// package), but is not part of the public API for cell authors. The archtest
// in tools/archtest/auth_plan_test.go (AUTH-PLAN-04) enforces that cells/ do
// not call it.
//
// Method is on value receiver: the internal atomic.Pointer is already a
// pointer, so the store is visible to all copies of this value.
func (p AuthJWTFromAssembly) SetResolved(v IntentTokenVerifier) {
	if p.resolved != nil {
		p.resolved.Store(&v)
	}
}

// Compile-time assertion.
var _ ListenerAuth = AuthJWTFromAssembly{}

// ─── AuthMTLS ─────────────────────────────────────────────────────────────────

// AuthMTLS is the mutual-TLS listener plan. The middleware asserts that the
// request arrived over a TLS connection with at least one peer certificate.
// Chain verification MUST be delegated to tls.Config.ClientAuth; see the
// runtime/bootstrap phase0 validation (validateAuthPlanMTLSBindings).
type AuthMTLS struct{}

func (AuthMTLS) authPlanKind() AuthKind { return AuthKindMTLS }
func (AuthMTLS) Describe() string       { return "mtls" }

// listenerAuthOK seals the ListenerAuth interface (see AuthNone.listenerAuthOK).
func (AuthMTLS) listenerAuthOK() {}

// Compile-time assertion.
var _ ListenerAuth = AuthMTLS{}

// ─── AuthServiceToken ─────────────────────────────────────────────────────────

// AuthServiceToken is the HMAC-SHA256 service token plan. Bootstrap installs
// auth.ServiceTokenMiddleware with the provided ring and nonce store.
type AuthServiceToken struct {
	// Store is the NonceStore for replay prevention. Required.
	Store NonceStore
	// Ring is the HMACKeyring supplying signing secrets. Required.
	Ring HMACKeyring
}

// NewAuthServiceToken constructs an AuthServiceToken plan. Panics if either
// argument is nil or if ring.Current() returns fewer than MinHMACKeyBytes bytes
// — both are required for a properly guarded internal listener; a short HMAC
// secret silently weakens MAC strength and is rejected at construction time
// (NIST SP 800-107 §5.3.4 — HMAC key length must match underlying hash
// security strength: 256-bit / 32-byte for HMAC-SHA-256).
func NewAuthServiceToken(store NonceStore, ring HMACKeyring) AuthServiceToken {
	if store == nil {
		panic("cell: NewAuthServiceToken store must not be nil")
	}
	if ring == nil {
		panic("cell: NewAuthServiceToken ring must not be nil")
	}
	if got := len(ring.Current()); got < MinHMACKeyBytes {
		panic(fmt.Sprintf(
			"cell: NewAuthServiceToken HMAC ring.Current() returned %d bytes, minimum is %d (NIST SP 800-107)",
			got, MinHMACKeyBytes))
	}
	return AuthServiceToken{Store: store, Ring: ring}
}

func (AuthServiceToken) authPlanKind() AuthKind { return AuthKindServiceToken }
func (AuthServiceToken) Describe() string       { return "service-token" }

// listenerAuthOK seals the ListenerAuth interface (see AuthNone.listenerAuthOK).
func (AuthServiceToken) listenerAuthOK() {}

// Compile-time assertion.
var _ ListenerAuth = AuthServiceToken{}
