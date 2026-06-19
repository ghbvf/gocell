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
// Gate: auth.RequirePermission(authz.PermDeviceRead()) — coarse admin/operator read.
// Device-self (query-param deviceId == subject) lands in PR-2.
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

	rd := statusv1.ResponseData{
		DeviceID: rec.DeviceID,
		CertRef: &statusv1.ResponseDataCertRef{
			Issuer: rec.Issuer,
			Serial: rec.Serial,
		},
		Status:    certStateToEnum(rec.State),
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
// http.deviceidentity.status.v1 (GET /api/v1/deviceidentity/status?deviceId=<string>)
// on the primary listener, gated by auth.RequirePermission(authz.PermDeviceRead()).
//
// The contract is ownerCell: _framework / lifecycle: draft. Serving it here is the
// interim §6.1 pattern (ADR-1939 D3): external cell serves draft contract; the
// framework active-ization PR is backlogged. The Group.CellID is intentionally
// empty (framework-owned, no cell binding per ADR-1939).
func (s *Service) FrameworkRoute() bootstrap.FrameworkServedRoute {
	h := statusv1.NewHandler(s, auth.RequirePermission(authz.PermDeviceRead()))
	return bootstrap.FrameworkServedRoute{
		ContractID: ContractID,
		Group: kcell.RouteGroup{
			Listener: kcell.PrimaryListener,
			Register: func(mux kcell.RouteMux) error { return h.RegisterRoutes(mux) },
		},
	}
}

var _ statusv1.Service = (*Service)(nil)
