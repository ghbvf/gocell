// Package deviceserving is the composition-root wiring for the framework-owned
// http.devicestate.v1 contract (ownerCell: _framework, ADR 202606130635-1939).
// A framework-owned contract has no cell, so the composition root — not a cell
// module — provides its Service implementation and the bootstrap RouteGroup.
//
// Honest baseline: GoCell has no device-presence backend yet, so the service
// reports state "unknown" with the determination time as observedAt. The
// devicestate response schema documents observedAt as always present even when
// state is unknown, so this is the contract-designed honest response — never a
// fabricated online/offline. When an MDM / presence provider is wired, swap this
// Service implementation; the contract, RouteGroup, and gate are unchanged.
package deviceserving

import (
	"context"
	"time"

	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/projection"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	devicestate "github.com/ghbvf/gocell/generated/contracts/http/devicestate/v1"
)

// devicestateContractID mirrors the generated devicestate contractSpec.ID. The
// generated package keeps its spec unexported, so the id is restated here; the
// bootstrap startup reconcile cross-checks it against
// generatedFrameworkServedContracts() (assembly.yaml frameworkContracts), so any
// drift fails fast at startup rather than serving a mismatched route.
const devicestateContractID = "http.devicestate.v1"

// Service implements the generated devicestate.Service for http.devicestate.v1.
type Service struct {
	clock clock.Clock
}

// NewService constructs the devicestate Service. clock is a positional
// dependency (go-standards: clock.Clock is positional, validated via
// MustHaveClock — never a WithClock option or config field).
func NewService(clk clock.Clock) *Service {
	clock.MustHaveClock(clk, "deviceserving.NewService")
	return &Service{clock: clk}
}

// Devicestate reports honest device presence. With no presence backend wired it
// returns state "unknown" with the determination time as the freshness anchor;
// it never fabricates online/offline.
func (s *Service) Devicestate(_ context.Context, req *devicestate.Request) (devicestate.DevicestateResponseObject, error) {
	rd := devicestate.ResponseData{
		DeviceID: req.ID,
		State:    devicestate.ResponseDataStateUnknown,
		// TenantID is intentionally left empty: without a device→tenant binding data
		// source, back-filling the caller's JWT tenant claim would be misleading (that
		// is the caller's tenant, not the device's tenant). Honest omission until a
		// real provider supplies the device tenant context.
		ObservedAt: s.clock.Now().UTC().Format(time.RFC3339),
	}
	// Identity projection: no field masking obligation on this read (matches the
	// other public read handlers, e.g. configread). The masked view's field set
	// equals the DTO's field set.
	data, err := projection.NewProjection(authz.IdentityFieldMask(), rd.ToMap())
	if err != nil {
		return nil, err
	}
	return devicestate.Devicestate200JSONResponse{Data: data}, nil
}

// Route builds the bootstrap.FrameworkServedRoute that mounts http.devicestate.v1
// (GET /api/v1/devicestate/{id}) on the primary listener, gated by
// auth.RequirePermissionForResource("id", device:read) — the device id path param
// is forwarded to the PDP as the ABAC `resource`, so the engine decides per-device
// ownership (baseline owner rule `subject.sub == resource.id`) rather than an
// all-or-nothing coarse gate. This is the documented PermDeviceRead() shape and
// matches the sibling devicecommand gate (#2348 F3 / #2351). The composition root
// passes generatedFrameworkServedContracts() as the must-serve expectation set
// alongside this route; bootstrap reconciles the two at startup.
//
// The platform (corecells/accesscore) PDP baseline grants no device:read rule yet,
// so on corebundle the endpoint fail-closes to deny until a tenant policy (or the
// presence-backend PR) supplies a device:read grant — the safe default for an
// MDM/zero-trust boundary. The device-ownership data-layer (RowScope/tenant
// isolation against real presence data) is tracked in #2351.
func (s *Service) Route() bootstrap.FrameworkServedRoute {
	h := devicestate.NewHandler(s, auth.RequirePermissionForResource("id", authz.PermDeviceRead()))
	return bootstrap.FrameworkServedRoute{
		ContractID: devicestateContractID,
		Group: kcell.RouteGroup{
			Listener: kcell.PrimaryListener,
			Register: func(mux kcell.RouteMux) error { return h.RegisterRoutes(mux) },
		},
	}
}
