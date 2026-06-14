package configcoretest

// authz.go — shared ABAC-PDP test helpers for the configcore permission-based
// authz migration (PR-10b #1348). Every configcore slice gated by
// auth.RequirePermission(authz.Perm*) needs an Authorizer in the request context
// to pass the gate; these helpers supply an action-capturing Authorizer type and
// the ctx wiring, shared here for slices that can import configcoretest.
//
// Note on circular-dependency stubs: slices whose test packages would create an
// import cycle through configcoretest (e.g. configwrite, because configcoretest
// imports configwrite) define equivalent local stubs in their own _test.go files
// rather than importing this package. Those stubs are structurally identical to
// CapturingAuthorizer and WithAuthorizer but are unexported and local to the slice
// under test.
//
// Verdict construction (Allow/Deny) was moved to each consumer's _test.go under
// local helpers (allowAuthorizer / withAllowAuthorizer / withDenyAuthorizer).
// AUTHZ-DECISION-ALLOW-DENY-CALLER-01 sanctions authz.Allow/Deny in _test.go
// files (test doubles) but not in non-test production-scanned packages; this
// file remains production-scanned so it must not call authz.Allow or authz.Deny.

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
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

// WithAuthorizer injects an Authorizer into ctx via the canonical
// auth.WithAuthorizer funnel (the same one the primary listener uses in
// production), so tests need not import runtime/auth just for the wiring.
func WithAuthorizer(ctx context.Context, a auth.Authorizer) context.Context {
	return auth.WithAuthorizer(ctx, a)
}
