package auth

import (
	"context"
	"slices"
)

type PrincipalKind int

const (
	PrincipalUser      PrincipalKind = iota // JWT user
	PrincipalService                        // service token / mTLS machine
	PrincipalAnonymous                      // public endpoint
)

func (k PrincipalKind) String() string {
	switch k {
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
// successful authentication. Claims is treated as read-only after construction;
// callers must not mutate the map (concurrent reads only).
//
// Principal supersedes the legacy Claims context value (see Claims in auth.go);
// AuthMiddleware will inject Principal once F7 wiring lands. New handlers should
// consume FromContext rather than reading Claims directly.
type Principal struct {
	Kind                  PrincipalKind
	Subject               string
	Roles                 []string
	AuthMethod            string
	PasswordResetRequired bool
	Claims                map[string]string
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
// well-known internal services. Dynamic, configurable service roles will be
// resolved via config.Registry once F1 lands; treat this as a temporary
// bootstrap until then.
func BuiltinServiceRoles(name string) []string {
	switch name {
	case ServiceNameInternal:
		return []string{RoleInternalAdmin}
	default:
		return nil
	}
}
