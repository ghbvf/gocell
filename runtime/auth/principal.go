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

type Principal struct {
	Kind       PrincipalKind
	Subject    string
	Roles      []string
	AuthMethod string
	Claims     map[string]string
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

func DefaultServiceRoles(name string) []string {
	switch name {
	case "gocell-internal":
		return []string{"role:internal-admin"}
	default:
		return nil
	}
}
