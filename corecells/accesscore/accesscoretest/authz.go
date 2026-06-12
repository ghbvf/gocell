package accesscoretest

// authz.go — shared ABAC-PDP test helpers for the accesscore permission-based
// authz migration (PR-10c #1348). Every accesscore slice gated by
// auth.RequirePermission(authz.Perm*) / auth.RequirePermissionOrSelf needs an
// Authorizer in the request context to pass the gate (non-self path); these
// helpers supply an action-capturing Authorizer type and the ctx wiring, shared
// here for slices and the cell-level production gate-lock test.
//
// Verdict construction (authz.Allow/Deny) lives in each consumer's _test.go under
// local helpers — AUTHZ-DECISION-ALLOW-DENY-CALLER-01 sanctions authz.Allow/Deny
// in _test.go files (test doubles) but not in non-test production-scanned packages;
// this file is production-scanned, so it must not call authz.Allow or authz.Deny.

import (
	"context"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
)

// CapturingAuthorizer is a test-only auth.Authorizer that returns a fixed
// Decision and records the (subject, resource, action) of the last Authorize
// call. GotAction lets a gate-lock test PIN the exact permission the route gate
// asked the PDP for (e.g. assert policymanage create demanded "policy:write", not
// "policy:read") — the only guard that catches an endpoint↔permission misbinding
// the role-agnostic baseline (admin allowed for every accesscore perm) would mask.
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

// WithAuthorizer injects an Authorizer into ctx via the canonical
// auth.WithAuthorizer funnel (the same one the primary listener uses in
// production), so tests need not import runtime/auth just for the wiring.
func WithAuthorizer(ctx context.Context, a auth.Authorizer) context.Context {
	return auth.WithAuthorizer(ctx, a)
}
