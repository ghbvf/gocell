package ordercell

import (
	"context"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// orderAuthorizer is the examples/todoorder example-owned lightweight PDP.
//
// Design notes (PR-10d #1894):
//  1. The platform PDP (corecells/accesscore/internal/abac) is not importable
//     from examples/ per the layer rules; examples own their own PDP when a
//     platform PDP is not wired in the assembly.
//  2. Per the PR-10d ADR "examples own PDP+baseline": each example cell ships
//     its own authorizer that encodes the minimal role baseline for that
//     example, keeping examples self-contained without dragging in accesscore.
//  3. This is the SOLE file in examples/todoorder where authz.Allow and
//     authz.Deny are called; all other files reach the PDP only via
//     auth.RequirePermission / auth.RequirePermissionForResource (which call
//     Authorize indirectly through the injected Authorizer). The archtest
//     caller-allowlist for the Allow/Deny funnel names this file.
//  4. order.Owner != order.ID (the path param): the path param is the order ID,
//     but ownership is determined by who *created* the order (JWT subject stored
//     in order.Owner). The authorizer performs a PIP lookup via repo.GetByID to
//     resolve the owner for order:read and order:update actions.
var _ auth.Authorizer = orderAuthorizer{}

// orderAuthorizer implements auth.Authorizer for the todoorder example.
// It holds a reference to the order repository for PIP ownership lookups.
type orderAuthorizer struct {
	repo domain.OrderRepository
}

// newOrderAuthorizer creates an orderAuthorizer backed by the given repository.
// The repo is used as a PIP to resolve order.Owner for ownership-scoped actions.
func newOrderAuthorizer(repo domain.OrderRepository) orderAuthorizer {
	return orderAuthorizer{repo: repo}
}

// allow is the shared constructor for an Allow Decision with zero obligations.
// Zero obligations mean the route gate can discharge the decision itself
// (no data-layer PEP is required), which is the normal path for this example.
func allow() (authz.Decision, error) {
	return authz.Allow(authz.Obligations{})
}

// Authorize evaluates the todoorder PDP baseline for the given subject, resource,
// and action. Called by the auth middleware (via auth.WithAuthorizer) on every
// request to the primary listener.
//
// Baseline rules:
//   - order:create → allow for role:customer.
//   - order:list   → allow for role:customer.
//   - order:read   → allow when subject == order.Owner (PIP: repo.GetByID(resource)).
//   - order:update → allow when subject == order.Owner (PIP: repo.GetByID(resource)).
//   - unknown action → deny (closed-set fail-closed).
//
// Ownership for read/update is role-agnostic: only subject==order.Owner matters,
// matching the accesscore baseline ownership pattern (#1977).
//
// Fail-closed: missing principal, empty subject for ownership, order not found,
// or no matching rule → deny without error (authentication precedes authorization
// at the route gate; store unavailability → deny, not error, for self-contained example).
func (a orderAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p == nil {
		return authz.Deny("order-authz: no authenticated principal"), nil
	}

	switch action {
	case authz.PermOrderCreate().String(), authz.PermOrderList().String():
		if p.HasRole(dto.RoleCustomer) {
			return allow()
		}
		return authz.Deny("order-authz: insufficient permissions"), nil

	case authz.PermOrderRead().String(), authz.PermOrderUpdate().String():
		if subject == "" {
			return authz.Deny("order-authz: empty subject"), nil
		}
		order, err := a.repo.GetByID(ctx, resource)
		if err != nil || order == nil {
			// Fail-closed: an ownership PIP lookup failure (not-found or repo error)
			// DENIES rather than surfacing a 5xx — for this self-contained example a
			// missing/unresolvable resource means ownership cannot be established, so
			// the gate must close, not fault. Intentional: returns a Deny verdict, not
			// the error. The deny reason is intentionally the same string as the
			// insufficient-permissions branch below to avoid leaking whether the order
			// exists (existence side-channel).
			return authz.Deny("order-authz: insufficient permissions"), nil //nolint:nilerr // fail-closed: ownership cannot be established → deny
		}
		if order.Owner != "" && order.Owner == subject {
			return allow()
		}
		return authz.Deny("order-authz: insufficient permissions"), nil

	default:
		return authz.Deny("order-authz: unknown action"), nil
	}
}
