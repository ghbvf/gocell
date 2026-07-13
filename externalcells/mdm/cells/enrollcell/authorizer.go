package enrollcell

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// MDM role constants. These are the role names the JWT issuer encodes in the
// "roles" claim for MDM principals. They follow the iotdevice example pattern
// (per-cell const, no shared platform role registry at this scope).
const (
	// RoleMDMAdmin is the fleet-admin role: can read/manage all device certificates.
	RoleMDMAdmin = "mdm-admin"
	// RoleMDMOperator is the fleet-operator role: read-only access to device status.
	RoleMDMOperator = "mdm-operator"
)

// enrollAuthorizer is the mdm-owned lightweight PDP (per PR-10d §"examples own PDP+baseline":
// external cells ship their own authorizer that encodes the minimal role baseline
// for that cell, keeping it self-contained without dragging in corecells/accesscore).
//
// Design notes:
//  1. Per plan §AI-robust: enrollAuthorizer.HasRole is the PDP baseline
//     (tenancy.md: "baseline is action-scoped + role-conditioned allow"),
//     NOT a handler-level authz branch. The PERMISSION-BASED-AUTHZ-01 archtest
//     scan does not cover externalcells/mdm (external-module pre-existing gap,
//     backlogged as #1090/M9), so this is safe by design, not vacuous.
//  2. This is the SOLE file in enrollcell where authz.Allow and authz.Deny are
//     called; all other files reach the PDP only via auth.RequirePermission.
//
// Fail-closed: any unknown action, missing principal, or insufficient role → deny.
var _ auth.Authorizer = enrollAuthorizer{}

// enrollAuthorizer implements auth.Authorizer with the MDM device-status baseline.
// It is stateless and safe to use from multiple goroutines.
type enrollAuthorizer struct{}

// allow is the shared constructor for an Allow Decision with zero obligations.
// Zero obligations mean the route gate can discharge the decision itself
// (no data-layer PEP is required for this coarse read).
func allow() (authz.Decision, error) {
	return authz.Allow(authz.Obligations{})
}

// Authorize evaluates the MDM PDP baseline for the given subject, resource, and action.
//
// Baseline rules (PR-1 coarse read scope; device-self gating lands in PR-2):
//   - device:read → allow for mdm-admin or mdm-operator (coarse fleet-read gate).
//   - unknown action → deny (closed-set fail-closed).
//
// Fail-closed: missing principal → deny without error (not a server error;
// authentication must precede authorization at the route gate).
func (enrollAuthorizer) Authorize(ctx context.Context, _, _, action string) (authz.Decision, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p == nil {
		return authz.Deny("mdm-authz: no authenticated principal"), nil
	}

	adminOrOperator := p.HasRole(RoleMDMAdmin) || p.HasRole(RoleMDMOperator)

	switch action {
	case authz.PermDeviceRead().String():
		if adminOrOperator {
			return allow()
		}
		return authz.Deny("mdm-authz: insufficient permissions for device:read"), nil
	default:
		return authz.Deny("mdm-authz: unknown action"), nil
	}
}
