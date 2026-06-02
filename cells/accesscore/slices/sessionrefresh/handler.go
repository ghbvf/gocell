package sessionrefresh

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	refreshgen "github.com/ghbvf/gocell/generated/contracts/http/auth/refresh/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// headerTenantID is the HTTP header name carrying the tenant identifier.
// The refresh endpoint is Public (no JWT), so the tenant must be supplied
// via this header for role lookup in MintAccess (#1337 PR-2).
const headerTenantID = "X-Tenant-ID"

// refreshTenantCtxKey is the unexported context key used by the tenant
// injection mux to ferry the X-Tenant-ID header value into the service ctx.
type refreshTenantCtxKey struct{}

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
	// Inject tenant from ctx key set by the tenantInjectMux middleware.
	// tenantInjectMux extracts X-Tenant-ID from the HTTP request header and
	// stores it in ctx. If present, inject into ctxkeys so tenant.FromContext
	// works in the service's MintAccess role lookup (#1337 PR-2).
	if tid, _ := ctx.Value(refreshTenantCtxKey{}).(string); tid != "" {
		ctx = ctxkeys.WithTenantID(ctx, tid)
	}
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

// ServeHTTP allows Handler to be used directly as an http.Handler in tests.
// It injects the X-Tenant-ID header value into the ctx before delegating.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if tid := r.Header.Get(headerTenantID); tid != "" {
		r = r.WithContext(ctxkeys.WithTenantID(r.Context(), tid))
	}
	h.refreshH.ServeHTTP(w, r)
}

// tenantInjectMiddleware is a middleware that injects the X-Tenant-ID HTTP
// header value into ctxkeys.TenantID before passing the request downstream.
// Required because the refresh endpoint is Public (no JWT), so the listener
// auth middleware does not populate ctxkeys.TenantID from a claim; the role
// lookup in MintAccess reads the tenant via tenant.FromContext (#1337 PR-2).
func tenantInjectMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tid := r.Header.Get(headerTenantID); tid != "" {
			r = r.WithContext(ctxkeys.WithTenantID(r.Context(), tid))
		}
		next.ServeHTTP(w, r)
	})
}

// RegisterRoutes mounts the refresh contract handler on mux, wrapping it
// with the tenant injection middleware so X-Tenant-ID is available in ctx.
// When mux implements cell.RouteMux (the production chi-backed router),
// With(middleware) is used to inject the tenant before the handler. Non-RouteMux
// values (e.g. test stubs implementing only RouteHandler) register without the
// middleware wrapper.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if rm, ok := mux.(kcell.RouteMux); ok {
		return h.refreshH.RegisterRoutes(rm.With(tenantInjectMiddleware))
	}
	return h.refreshH.RegisterRoutes(mux)
}
