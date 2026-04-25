package cell

// auth_plan.go — sealed AuthPlan interface + 6 typed plan structs.
//
// Design goals:
//   1. Sealed via unexported marker methods (authPlanKind/listenerAuthOK/groupAuthOK).
//   2. Two sub-interfaces segregate usage sites at compile time:
//      ListenerAuth — plans valid as the default policy on a physical listener.
//      GroupAuth    — plans valid as the policy override on a RouteGroup.
//   3. AuthJWT/AuthJWTFromAssembly implement only ListenerAuth (not GroupAuth)
//      because the JWT verifier must flow through the router's matcher-aware
//      AuthMiddleware chain; direct RouteGroup use would silently drop the verifier.
//   4. AuthVerboseToken implements only GroupAuth (not ListenerAuth) because it
//      is a per-endpoint ?verbose guard, not a listener-wide auth scheme.
//   5. AuthNone/AuthMTLS/AuthServiceToken implement both, giving callers maximum
//      composability.
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
	"crypto/sha256"
	"sync/atomic"
)

// AuthKind is the discriminant for an AuthPlan variant.
// Declared as uint8 to keep the type small; the iota values are stable.
type AuthKind uint8

const (
	AuthKindNone            AuthKind = iota // AuthNone
	AuthKindJWT                             // AuthJWT
	AuthKindJWTFromAssembly                 // AuthJWTFromAssembly
	AuthKindMTLS                            // AuthMTLS
	AuthKindServiceToken                    // AuthServiceToken
	AuthKindVerboseToken                    // AuthVerboseToken
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

// ListenerAuth is the sub-interface for plans that can serve as the default
// authentication scheme on a physical HTTP listener. JWT plans live here only;
// AuthVerboseToken does NOT implement ListenerAuth.
type ListenerAuth interface {
	AuthPlan
	listenerAuthOK() // compile-time segregation marker
}

// GroupAuth is the sub-interface for plans that can serve as the per-RouteGroup
// auth override. AuthJWT / AuthJWTFromAssembly do NOT implement GroupAuth —
// JWT must flow through the router's matcher-aware AuthMiddleware installed at
// listener level.
type GroupAuth interface {
	AuthPlan
	groupAuthOK() // compile-time segregation marker
}

// ─── AuthNone ────────────────────────────────────────────────────────────────

// AuthNone is the no-op auth plan. Use it for listeners/groups that are
// network-isolated and require no authentication gate (e.g. the HealthListener
// behind a Kubernetes probe path).
//
// AuthNone implements both ListenerAuth and GroupAuth.
type AuthNone struct{}

func (AuthNone) authPlanKind() AuthKind { return AuthKindNone }
func (AuthNone) Describe() string       { return "none" }
func (AuthNone) listenerAuthOK()        {}
func (AuthNone) groupAuthOK()           {}

// Compile-time assertions.
var _ ListenerAuth = AuthNone{}
var _ GroupAuth = AuthNone{}

// ─── AuthJWT ─────────────────────────────────────────────────────────────────

// AuthJWT is the JWT-authenticated listener plan. The verifier is supplied at
// construction time via NewAuthJWT. Bootstrap extracts it during phase5 and
// installs the router-aware AuthMiddleware so that Public/PasswordResetExempt
// routes declared via auth.Mount are honoured.
//
// AuthJWT implements only ListenerAuth. It intentionally does NOT implement
// GroupAuth — installing JWT at RouteGroup level would silently drop the
// verifier from the router chain.
type AuthJWT struct {
	// Verifier is the IntentTokenVerifier used to validate JWTs. Required; nil
	// is rejected by NewAuthJWT with a panic.
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
func (AuthJWT) listenerAuthOK()        {}

// Compile-time assertion.
var _ ListenerAuth = AuthJWT{}

// ─── AuthJWTFromAssembly ──────────────────────────────────────────────────────

// AssemblyRef is a minimal interface used by AuthJWTFromAssembly to hold a
// reference to a CoreAssembly without importing kernel/assembly (which would
// create an import cycle). Bootstrap passes the concrete *assembly.CoreAssembly
// which satisfies this interface; identity checks are done in bootstrap.
//
// The interface exposes only what kernel needs: nothing — it is a marker type
// used purely for identity comparison in bootstrap.phase0. Using the empty
// interface would lose the type safety; a named marker interface is explicit
// about intent.
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
func (AuthJWTFromAssembly) Describe() string       { return "jwt-from-assembly" }
func (AuthJWTFromAssembly) listenerAuthOK()        {}

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
//
// AuthMTLS implements both ListenerAuth and GroupAuth.
type AuthMTLS struct{}

func (AuthMTLS) authPlanKind() AuthKind { return AuthKindMTLS }
func (AuthMTLS) Describe() string       { return "mtls" }
func (AuthMTLS) listenerAuthOK()        {}
func (AuthMTLS) groupAuthOK()           {}

// Compile-time assertions.
var _ ListenerAuth = AuthMTLS{}
var _ GroupAuth = AuthMTLS{}

// ─── AuthServiceToken ─────────────────────────────────────────────────────────

// AuthServiceToken is the HMAC-SHA256 service token plan. Bootstrap installs
// auth.ServiceTokenMiddleware with the provided ring and nonce store.
//
// AuthServiceToken implements both ListenerAuth and GroupAuth.
type AuthServiceToken struct {
	// Store is the NonceStore for replay prevention. Required.
	Store NonceStore
	// Ring is the HMACKeyring supplying signing secrets. Required.
	Ring HMACKeyring
}

// NewAuthServiceToken constructs an AuthServiceToken plan.
// Panics if either argument is nil — both are required for a properly guarded
// internal listener.
func NewAuthServiceToken(store NonceStore, ring HMACKeyring) AuthServiceToken {
	if store == nil {
		panic("cell: NewAuthServiceToken store must not be nil")
	}
	if ring == nil {
		panic("cell: NewAuthServiceToken ring must not be nil")
	}
	return AuthServiceToken{Store: store, Ring: ring}
}

func (AuthServiceToken) authPlanKind() AuthKind { return AuthKindServiceToken }
func (AuthServiceToken) Describe() string       { return "service-token" }
func (AuthServiceToken) listenerAuthOK()        {}
func (AuthServiceToken) groupAuthOK()           {}

// Compile-time assertions.
var _ ListenerAuth = AuthServiceToken{}
var _ GroupAuth = AuthServiceToken{}

// ─── AuthVerboseToken ─────────────────────────────────────────────────────────

// AuthVerboseToken is the ?verbose-mode access guard. When a request carries
// the ?verbose query parameter, the request must supply a matching token in the
// configured header. Requests without ?verbose pass through unconditionally.
//
// Intended for the /readyz RouteGroup only. AuthVerboseToken implements only
// GroupAuth — it is semantically a per-route guard, not a listener-wide scheme.
//
// The configured token is stored as its SHA-256 hash so raw token bytes are
// never held in memory after construction (SEC-06 defense-in-depth).
type AuthVerboseToken struct {
	// Header is the HTTP header name carrying the token (e.g. "X-Readyz-Token").
	Header string
	// HashedToken is the SHA-256 hash of the configured token for constant-time
	// comparison (avoids timing oracle on the raw token length).
	HashedToken [32]byte
}

// NewAuthVerboseToken constructs an AuthVerboseToken plan.
// Panics if headerName or token is empty — both are required.
func NewAuthVerboseToken(headerName, token string) AuthVerboseToken {
	if headerName == "" {
		panic("cell: NewAuthVerboseToken headerName must not be empty")
	}
	if token == "" {
		panic("cell: NewAuthVerboseToken token must not be empty")
	}
	return AuthVerboseToken{
		Header:      headerName,
		HashedToken: sha256.Sum256([]byte(token)),
	}
}

func (AuthVerboseToken) authPlanKind() AuthKind { return AuthKindVerboseToken }
func (AuthVerboseToken) Describe() string       { return "verbose-token" }
func (AuthVerboseToken) groupAuthOK()           {}

// Compile-time assertion.
var _ GroupAuth = AuthVerboseToken{}

// NOTE: AuthVerboseToken intentionally does NOT implement ListenerAuth.
// The archtest in tools/archtest/auth_plan_test.go verifies this at CI time.
