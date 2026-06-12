package configcoretest

// authz.go — shared ABAC-PDP test helpers for the configcore permission-based
// authz migration (PR-10b #1348). Every configcore slice gated by
// auth.RequirePermission(authz.Perm*) needs an Authorizer in the request context
// to pass the gate; these helpers supply allow / deny / action-capturing
// Authorizers and the ctx wiring, defined ONCE here instead of per-slice.

import (
	"context"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
)

// CapturingAuthorizer is a test-only auth.Authorizer that returns a fixed
// Decision and records the (subject, resource, action) of the last Authorize
// call. GotAction lets a handler test PIN the exact permission the route gate
// asked the PDP for (e.g. assert configwrite demanded "config:write", not
// "config:read") — the only guard that catches an endpoint↔permission misbinding
// the role-agnostic baseline (admin allowed for every config perm) would mask.
type CapturingAuthorizer struct {
	Decision    authz.Decision
	Err         error
	GotSubject  string
	GotResource string
	GotAction   string
}

// Authorize records the call and returns the fixed Decision/Err.
func (c *CapturingAuthorizer) Authorize(_ context.Context, subject, resource, action string) (authz.Decision, error) {
	c.GotSubject, c.GotResource, c.GotAction = subject, resource, action
	return c.Decision, c.Err
}

// AllowAuthorizer returns a CapturingAuthorizer that grants every request
// (Decision = Allow with no obligations), mirroring a wired PDP that permits.
func AllowAuthorizer() *CapturingAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("configcoretest.AllowAuthorizer: authz.Allow: " + err.Error())
	}
	return &CapturingAuthorizer{Decision: dec}
}

// DenyAuthorizer returns a CapturingAuthorizer that denies every request with
// the given reason — used to assert a wired PDP's deny surfaces as 403.
func DenyAuthorizer(reason string) *CapturingAuthorizer {
	return &CapturingAuthorizer{Decision: authz.Deny(reason)}
}

// WithAuthorizer injects an Authorizer into ctx via the canonical
// auth.WithAuthorizer funnel (the same one the primary listener uses in
// production), so tests need not import runtime/auth just for the wiring.
func WithAuthorizer(ctx context.Context, a auth.Authorizer) context.Context {
	return auth.WithAuthorizer(ctx, a)
}

// WithAllowAuthorizer wraps ctx with an allow-all Authorizer (success path).
func WithAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, AllowAuthorizer())
}

// WithDenyAuthorizer wraps ctx with a deny-all Authorizer (PDP-deny → 403 path).
func WithDenyAuthorizer(ctx context.Context, reason string) context.Context {
	return auth.WithAuthorizer(ctx, DenyAuthorizer(reason))
}
