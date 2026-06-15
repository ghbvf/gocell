// Package sessionprojection implements the public-facing session-projection slice.
package sessionprojection

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	registrysummary "github.com/ghbvf/gocell/generated/contracts/http/session/registry-summary/v1"
)

// Compile-time assertion: SummaryAdapter implements the generated Service interface.
var _ registrysummary.Service = (*SummaryAdapter)(nil)

// SummaryAdapter bridges Service to the generated registrysummary.Service interface.
//
// Tenant isolation: the adapter reads the authenticated principal's TenantID to
// scope the count query. An unauthenticated caller (no principal) receives 401;
// an authenticated principal with no tenant scope receives 403 (fail-closed).
// The response exposes only the count of sessions for the caller's tenant —
// NOT any raw sessionId or userId — to avoid leaking session identity data.
type SummaryAdapter struct {
	svc *Service
}

// NewSummaryAdapter creates a SummaryAdapter.
func NewSummaryAdapter(s *Service) *SummaryAdapter {
	return &SummaryAdapter{svc: s}
}

// RegistrySummary implements registrysummary.Service.
//
// Security properties:
//   - No principal → 401 (authentication required).
//   - Principal with empty TenantID → 403 (tenant-scoped read, fail-closed).
//     This mirrors the auditquery handler's empty-tenant 403 gate (epic #1337 PR-2a, F1).
//   - Authorized principal → returns only the session count for that tenant.
//     Raw sessionId/userId are never included in the response (privacy boundary;
//     analogous to todoorder SummaryAdapter per-status aggregate pattern).
func (a *SummaryAdapter) RegistrySummary(
	ctx context.Context, _ *registrysummary.Request,
) (registrysummary.RegistrySummaryResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return registrysummary.RegistrySummary401ErrorResponse{
			Body: *errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
				"authentication required"),
		}, nil
	}

	// Tenant isolation fail-closed: a session registry read requires a concrete
	// tenant. An authenticated principal with an empty TenantID cannot establish
	// an isolation scope; returning only the count for an empty-string tenant key
	// would silently produce wrong/zero data rather than the caller's intended
	// tenant view. Reject here with 403 so the caller (and operator) understand
	// the isolation constraint was not satisfied.
	if p.TenantID == "" {
		return registrysummary.RegistrySummary403ErrorResponse{
			Body: *errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
				"session registry-summary requires a tenant-scoped principal"),
		}, nil
	}

	tid, err := tenant.ParseTenantID(p.TenantID)
	if err != nil {
		// Non-canonical TenantID: the JWT authenticator should have canonicalised
		// it, so this is an internal invariant break → return framework 5xx path.
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"session registry-summary: invalid tenant in principal", err)
	}

	n := a.svc.Query(ctx, tid)
	return registrysummary.RegistrySummary200JSONResponse(registrysummary.Response{
		Data: &registrysummary.ResponseData{
			TotalSessions: n,
		},
	}), nil
}
