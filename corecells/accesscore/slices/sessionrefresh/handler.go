package sessionrefresh

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/httpcookie"
	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/runtime/http/cellmw"
	refreshgen "github.com/ghbvf/gocell/generated/contracts/http/auth/refresh/v1"
)

// RefreshAdapter implements refreshgen.Service for http.auth.refresh.v1.
// It adapts the slice-internal Service (Refresh takes a raw token string)
// to the generated interface (Refresh takes *refreshgen.Request).
type RefreshAdapter struct{ S *Service }

// Refresh implements refreshgen.Service. The generated handler already validates
// and decodes refreshToken from the request body.
//
// Token source (cookie-first, body-fallback — BR-005 #1278):
//  1. If the inbound __Host-gocell_rt cookie is present, its value is used as the
//     refresh token; the request body refreshToken field is ignored.
//  2. Otherwise the body refreshToken field is used.
//
// On success a refreshed Set-Cookie is emitted via the httpcookie middleware so
// the browser's httpOnly jar is always kept current.
//
// All rejections (revoked/subject-mismatch/user-not-active/stale-epoch/reuse)
// surface as 401 via framework fallback (ADR §A13 single-envelope); 503 is
// reserved for infra outages that prevent evaluation of the request.
//
// Why every declared status here goes through (nil, err) framework fallback
// rather than typed Refresh{401,503}ErrorResponse structs: ADR D7 makes
// ADAPTER-RETURNS-DECLARED-TYPES-01 a *ceiling* — returning zero typed error
// structs is legal; the archtest only rejects returning an *un*declared typed
// status, it never forces declared statuses to be typed. Both paths apply the
// same status-aware redaction (httputil.WriteError derives status + strips 5xx
// details from errcode.Kind exactly as the generated typed path does), so for
// an endpoint whose errors carry no per-status business body beyond the shared
// envelope the framework path is the simpler equivalent — not a redaction gap.
// (Pre-existing convention; the 403 typed branch removed in #940 was the lone
// exception and is now gone.)
func (a RefreshAdapter) Refresh(ctx context.Context, req *refreshgen.Request) (refreshgen.RefreshResponseObject, error) {
	// Cookie-first, body-fallback (BR-005).
	token := httpcookie.IncomingRefresh(ctx)
	if token == "" {
		token = req.RefreshToken
	}
	pair, err := a.S.Refresh(ctx, token)
	if err != nil {
		return nil, err
	}
	httpcookie.SetRefresh(ctx, pair.RefreshToken)
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
	refreshH  *refreshgen.Handler
	cookieTTL time.Duration
}

// NewHandler creates a sessionrefresh Handler using the generated refresh handler.
// cookieTTL is the refresh-token lifetime used as the Max-Age on the Set-Cookie
// header (BR-005 #1278). No policy argument: the refresh endpoint is Public (no
// JWT required).
func NewHandler(svc *Service, cookieTTL time.Duration) *Handler {
	return &Handler{
		refreshH:  refreshgen.NewHandler(RefreshAdapter{svc}),
		cookieTTL: cookieTTL,
	}
}

// RegisterRoutes mounts the refresh contract handler on mux.
// The refresh endpoint is Public (no JWT required). Tenant is derived from the
// refreshed user's TenantID field (#1337 PR-2 stopgap; PR-3 will carry tenant
// in the session/refresh row for true RLS isolation).
//
// The cookie middleware is applied via cellmw.NewHeaderInjectMux so that every
// registered route gets httpcookie.Middleware applied at the mux level.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.refreshH.RegisterRoutes(cellmw.NewHeaderInjectMux(mux, httpcookie.Middleware(h.cookieTTL)))
}
