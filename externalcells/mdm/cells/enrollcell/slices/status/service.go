package status

import (
	"context"
	"time"

	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/projection"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	statusv1 "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/status/v1"
)

// ContractID mirrors the generated contractSpec.ID for http.deviceidentity.status.v1.
// Exported so cmd/mdmd/main.go can reference it in mustServeFrameworkContracts()
// and the drift test in cmd/mdmd/framework_serving_test.go can obtain Source-B from
// status.NewService(...).FrameworkRoute().ContractID without duplicating the literal.
// (ADR-1939 §AI-robust: hand-authored []string must pair with a real Source-B assertion
// to qualify as Medium guard.)
const ContractID = "http.deviceidentity.status.v1"

// Service implements the generated statusv1.Service for http.deviceidentity.status.v1.
// It is the L0 status read layer: reads from the in-memory cert repository,
// projects via the identity field mask, and returns typed response envelopes.
//
// Gate: auth.RequirePermissionForResource("deviceId", authz.PermDeviceRead()) — the
// owner-scoped device:read shape (the deviceId path param is forwarded to the PDP as
// the ABAC resource, matching cellmodules/deviceserving). The accesscore baseline
// grants device:read to admin/super-admin (fleet read) and to the device itself
// (device-self); device-self reads await device-token auth in PR-2.
type Service struct {
	repo Repository
	clk  clock.Clock
}

// NewService constructs the status Service. Both repo and clk are mandatory.
// clk is positional (go-standards: clock.Clock is positional + MustHaveClock).
func NewService(repo Repository, clk clock.Clock) *Service {
	clock.MustHaveClock(clk, "status.NewService")
	if repo == nil {
		panic(panicregister.Approved("status-newservice-nil-repo", errcode.Assertion("status.NewService: repo must not be nil")))
	}
	return &Service{repo: repo, clk: clk}
}

// Status implements statusv1.Service.Status. It looks up the highest-epoch
// CertRecord for req.DeviceID:
//   - not found → Status404ErrorResponse
//   - found     → Status200JSONResponse with full schema (certRef, status enum,
//     RFC3339 times, renewalTime null when nil)
//
// The 400 (missing deviceId) case is handled upstream by the generated handler.
//
// 404 vs 403 information boundary: admin/operator coarse reads return 404 (not found)
// when a device is unknown; this is intentional for fleet-operations visibility
// (operators need to know whether a device exists). PR-2 device-self reads must
// re-evaluate whether 404 leaks existence to the requesting device — at that point
// the decision is whether to return 404 or 403 for non-existent-or-not-owned resources.
func (s *Service) Status(ctx context.Context, req *statusv1.Request) (statusv1.StatusResponseObject, error) {
	rec, ok, err := s.repo.ActiveByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return statusv1.Status404ErrorResponse{
			Body: *errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound,
				"device certificate not found"),
		}, nil
	}

	// Map the domain state to the wire enum. An unknown / zero state has no valid enum
	// member, so fail closed with a framework 5xx rather than emit a schema-invalid 200
	// with status:"" (#2426 F5).
	stateEnum, known := certStateToEnum(rec.State)
	if !known {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"device certificate has unknown state")
	}

	rd := statusv1.ResponseData{
		DeviceID: rec.DeviceID,
		CertRef: &statusv1.ResponseDataCertRef{
			Issuer: rec.Issuer,
			Serial: rec.Serial,
		},
		Status:    stateEnum,
		NotBefore: rec.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:  rec.NotAfter.UTC().Format(time.RFC3339),
		Epoch:     rec.Epoch,
	}
	if rec.RenewalTime != nil {
		t := rec.RenewalTime.UTC().Format(time.RFC3339)
		rd.RenewalTime = &t
	}

	data, err := projection.NewProjection(authz.IdentityFieldMask(), rd.ToMap())
	if err != nil {
		return nil, err
	}
	return statusv1.Status200JSONResponse{Data: data}, nil
}

// FrameworkRoute builds the bootstrap.FrameworkServedRoute that mounts
// http.deviceidentity.status.v1 (GET /api/v1/deviceidentity/status/{deviceId})
// on the primary listener, gated by
// auth.RequirePermissionForResource("deviceId", authz.PermDeviceRead()) — the
// owner-scoped gate forwards the canonical deviceId to the PDP as the ABAC resource
// (matching cellmodules/deviceserving), so the engine decides per-device ownership
// rather than an all-or-nothing coarse gate. The Group.CellID is intentionally
// empty (framework-owned, no cell binding per ADR-1939).
//
// The contract is ownerCell: _framework / lifecycle: draft; flipping it to active
// (platform corebundle serve + journey) is tracked in #2431, blocked-by the
// framework-owned HTTP permission-overlay path #2403. Until then mdm is the sole
// server of this route.
func (s *Service) FrameworkRoute() bootstrap.FrameworkServedRoute {
	h := statusv1.NewHandler(s, auth.RequirePermissionForResource("deviceId", authz.PermDeviceRead()))
	return bootstrap.FrameworkServedRoute{
		ContractID: ContractID,
		Group: kcell.RouteGroup{
			Listener: kcell.PrimaryListener,
			Register: func(mux kcell.RouteMux) error { return h.RegisterRoutes(mux) },
		},
	}
}

var _ statusv1.Service = (*Service)(nil)
