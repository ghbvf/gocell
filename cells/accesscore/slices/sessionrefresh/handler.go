package sessionrefresh

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	refreshgen "github.com/ghbvf/gocell/generated/contracts/http/auth/refresh/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
)

// RefreshAdapter implements refreshgen.Service for http.auth.refresh.v1.
// It adapts the slice-internal Service (Refresh takes a raw token string)
// to the generated interface (Refresh takes *refreshgen.Request).
type RefreshAdapter struct{ S *Service }

// Refresh implements refreshgen.Service. The generated handler already validates
// and decodes refreshToken from the request body.
//
// All rejections (revoked/subject-mismatch/user-not-active/stale-epoch/reuse)
// surface as 401 via framework fallback (ADR §A13 single-envelope); 503 is
// reserved for infra outages that prevent evaluation of the request.
//
// Why every declared status here goes through (nil, err) framework fallback
// rather than typed Refresh{401,503}ErrorResponse structs: the refresh endpoint
// carries no per-status business body beyond the shared error envelope, and
// httputil.WriteError owns the status-aware redaction (5xx strips details) that
// a hand-built typed struct could silently bypass. The status set is still
// statically guarded — ADAPTER-RETURNS-DECLARED-TYPES-01 rejects returning an
// *un*declared typed status; it does not force declared statuses to be typed.
// (Pre-existing convention; the 403 typed branch removed in #940 was the lone
// exception and is now gone.)
func (a RefreshAdapter) Refresh(ctx context.Context, req *refreshgen.Request) (refreshgen.RefreshResponseObject, error) {
	pair, err := a.S.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return nil, err
	}
	return refreshgen.Refresh200JSONResponse{
		Data: toRefreshResponseData(pair),
	}, nil
}

// toRefreshResponseData converts an internal TokenPair to the generated contract DTO.
func toRefreshResponseData(p dto.TokenPair) *refreshgen.ResponseData {
	return &refreshgen.ResponseData{
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		SessionID:             p.SessionID,
		UserID:                p.UserID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}

// Handler is the route handler for the sessionrefresh slice.
// The generated handler emits Public:true so no JWT is required for this route.
type Handler struct {
	refreshH *refreshgen.Handler
}

// NewHandler creates a sessionrefresh Handler using the generated refresh handler.
// No policy argument: the refresh endpoint is Public (no JWT required).
func NewHandler(svc *Service) *Handler {
	return &Handler{
		refreshH: refreshgen.NewHandler(RefreshAdapter{svc}),
	}
}

// RegisterRoutes mounts the refresh contract handler on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.refreshH.RegisterRoutes(mux)
}
