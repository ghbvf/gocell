package auth

import (
	"context"
	"slices"
)

type PrincipalKind int

const (
	// PrincipalUnknown is the zero value of PrincipalKind; it indicates an
	// uninitialised Principal and must never appear in a fully-constructed
	// Principal returned by an Authenticator.
	PrincipalUnknown   PrincipalKind = iota
	PrincipalUser                    // JWT user
	PrincipalService                 // service token / mTLS machine
	PrincipalAnonymous               // public endpoint
)

func (k PrincipalKind) String() string {
	switch k {
	case PrincipalUnknown:
		return "unknown"
	case PrincipalUser:
		return "user"
	case PrincipalService:
		return "service"
	case PrincipalAnonymous:
		return "anonymous"
	default:
		return "unknown"
	}
}

// Principal is the unified authn subject injected into request context after
// successful authentication. AuthMiddleware and ServiceTokenMiddleware both
// populate Principal via WithPrincipal; handlers consume it via FromContext.
// Principal is the authoritative authn context for all business routes (F1/F7).
type Principal struct {
	Kind                  PrincipalKind
	Subject               string
	Roles                 []string
	AuthMethod            string
	PasswordResetRequired bool
	// Claims is a read-only snapshot of supplementary JWT claims (e.g. "sid",
	// "iss", "token_use"). Callers must treat Claims as a read-only snapshot;
	// mutating it has no effect on authentication decisions and may corrupt
	// shared state.
	Claims map[string]string
}

// HasRole is nil-safe: a nil receiver always returns false.
func (p *Principal) HasRole(role string) bool {
	if p == nil || role == "" {
		return false
	}
	return slices.Contains(p.Roles, role)
}

// principalKey uses a private struct type to prevent collision with other packages.
type principalKey struct{}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func FromContext(ctx context.Context) (*Principal, bool) {
	v := ctx.Value(principalKey{})
	if v == nil {
		return nil, false
	}
	p, _ := v.(*Principal)
	if p == nil {
		return nil, false
	}
	if p.Kind == PrincipalUnknown {
		// Zero-value Principal was stored; treat as absent to prevent
		// uninitialised structs from leaking through as valid principals.
		return nil, false
	}
	return p, true
}

func MustFromContext(ctx context.Context) *Principal {
	p, ok := FromContext(ctx)
	if !ok {
		panic("auth: principal not in context")
	}
	return p
}

const (
	// ServiceNameInternal is the well-known name of the internal service principal
	// used by /internal/v1/* delegated auth (F4 RouteGroup will reference this).
	ServiceNameInternal = "gocell-internal"

	// RoleInternalAdmin grants admin access on internal control-plane endpoints.
	RoleInternalAdmin = "role:internal-admin"
)

// BuiltinServiceRoles returns the compile-time hard-coded role set for
// well-known internal services. Dynamic, configurable service roles are
// resolved via config.Registry (F1, PR#197); the compile-time set here covers
// bootstrap and test scenarios where the registry is not available.
func BuiltinServiceRoles(name string) []string {
	switch name {
	case ServiceNameInternal:
		return []string{RoleInternalAdmin}
	default:
		return nil
	}
}
