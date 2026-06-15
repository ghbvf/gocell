package devicecell

import (
	"context"

	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// deviceAuthorizer is the examples/iotdevice example-owned lightweight PDP.
//
// Design notes (PR-10d #1894):
//  1. The platform PDP (corecells/accesscore/internal/abac) is not importable
//     from examples/ per the layer rules; examples own their own PDP when a
//     platform PDP is not wired in the assembly.
//  2. Per the PR-10d ADR "examples own PDP+baseline": each example cell ships
//     its own authorizer that encodes the minimal role baseline for that
//     example, keeping examples self-contained without dragging in accesscore.
//  3. This is the SOLE file in examples/iotdevice where authz.Allow and
//     authz.Deny are called; all other files reach the PDP only via
//     auth.RequirePermission / auth.RequirePermissionForResource (which call
//     Authorize indirectly through the injected Authorizer). The archtest
//     caller-allowlist for the Allow/Deny funnel names this file.
var _ auth.Authorizer = deviceAuthorizer{}

// deviceAuthorizer implements auth.Authorizer with the iotdevice baseline.
// It is stateless and safe to use from multiple goroutines.
type deviceAuthorizer struct{}

// allow is the shared constructor for an Allow Decision with zero obligations.
// Zero obligations mean the route gate can discharge the decision itself
// (no data-layer PEP is required), which is the normal path for this example.
func allow() (authz.Decision, error) {
	return authz.Allow(authz.Obligations{})
}

// Authorize evaluates the device PDP baseline for the given subject, resource,
// and action. It is called by the auth middleware (via auth.WithAuthorizer) on
// every request to the primary listener.
//
// Baseline rules:
//   - device:command  → allow for admin or operator (coarse fleet command gate).
//   - device:consume  → allow for admin, operator, OR the device itself
//     (subject == resource, i.e. the device reads its own command queue).
//   - device:read     → same ownership rule as device:consume (device reads its
//     own status; admin/operator read any device's status).
//   - device:list     → allow for admin only (fleet enumeration is admin-gated).
//   - unknown action  → deny (closed-set fail-closed).
//
// Fail-closed: missing principal → deny without error (not a server error;
// authentication must precede authorization at the route gate).
func (deviceAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p == nil {
		return authz.Deny("device-authz: no authenticated principal"), nil
	}

	opOrAdmin := p.HasRole(dto.RoleAdmin) || p.HasRole(dto.RoleOperator)

	switch action {
	case authz.PermDeviceCommand().String():
		if opOrAdmin {
			return allow()
		}
		return authz.Deny("device-authz: insufficient permissions"), nil

	case authz.PermDeviceConsume().String(), authz.PermDeviceRead().String():
		// device:consume and device:read share the same ownership baseline:
		// admin/operator can access any device; the device itself (subject == resource)
		// can access its own queue/status. The cases are intentionally merged because
		// the baseline rules are identical; if future policy needs to differentiate
		// them (e.g. restricting status reads further), split into separate cases.
		if opOrAdmin || (subject != "" && subject == resource) {
			return allow()
		}
		return authz.Deny("device-authz: insufficient permissions"), nil

	case authz.PermDeviceList().String():
		if p.HasRole(dto.RoleAdmin) {
			return allow()
		}
		return authz.Deny("device-authz: insufficient permissions"), nil

	default:
		return authz.Deny("device-authz: unknown action"), nil
	}
}
