package sessionlogin

import (
	"context"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/httpcookie"
	logingen "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/cellmw"
)

// headerTenantID is the HTTP header name that carries the tenant identifier
// on the public login endpoint (pre-auth, so no JWT claim is available).
// Callers must set X-Tenant-ID to the canonical UUID of the tenant.
const headerTenantID = "X-Tenant-ID"

// loginTenantCtxKey is the unexported context key used by the tenantMiddleware
// to ferry the X-Tenant-ID header value into the generated handler's ctx.
type loginTenantCtxKey struct{}

// tenantFromLoginCtx reads the tenant ID string stored by tenantMiddleware.
// Returns "" if not set.
func tenantFromLoginCtx(ctx context.Context) string {
	v, _ := ctx.Value(loginTenantCtxKey{}).(string)
	return v
}

// LoginAdapter implements logingen.Service for http.auth.login.v1.
// It adapts the slice-internal Service (Login takes LoginInput) to the
// generated interface (Login takes *logingen.Request). The tenant ID is
// carried via ctx (injected by the wrapping Handler.ServeHTTP middleware
// from the X-Tenant-ID HTTP header).
type LoginAdapter struct{ S *Service }

// Login implements logingen.Service. The generated handler already validates
// and decodes username+password from the request body; the tenant comes from
// ctx (set by Handler.ServeHTTP from X-Tenant-ID header).
func (a LoginAdapter) Login(ctx context.Context, req *logingen.Request) (logingen.LoginResponseObject, error) {
	pair, err := a.S.Login(ctx, LoginInput{
		Username: req.Username,
		Password: req.Password,
		TenantID: tenantFromLoginCtx(ctx),
	})
	if err != nil {
		return nil, err
	}
	httpcookie.SetRefresh(ctx, pair.RefreshToken)
	return logingen.Login201JSONResponse{
		Data: toLoginResponseData(pair),
	}, nil
}

// toLoginResponseData converts an internal TokenPair to the generated contract DTO.
func toLoginResponseData(p dto.TokenPair) *logingen.ResponseData {
	return &logingen.ResponseData{
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		SessionID:             p.SessionID,
		UserID:                p.UserID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}

// Handler is the route handler for the sessionlogin slice.
// The generated handler emits Public:true so no JWT is required for this route.
// Handler wraps the generated logingen.Handler to inject the X-Tenant-ID header
// value into ctx before dispatching to LoginAdapter.Login, and wraps the
// response with httpcookie.Middleware to emit the refresh token as an httpOnly
// cookie on 201 responses (BR-005, #1278).
type Handler struct {
	loginH    *logingen.Handler
	cookieTTL time.Duration
}

// NewHandler creates a sessionlogin Handler using the generated login handler.
// cookieTTL is the refresh-token lifetime used as the cookie Max-Age on a
// successful login (e.g. 7*24*time.Hour for 7 days). No policy argument: the
// login endpoint is Public (no JWT required).
func NewHandler(svc *Service, cookieTTL time.Duration) *Handler {
	return &Handler{
		loginH:    logingen.NewHandler(LoginAdapter{svc}),
		cookieTTL: cookieTTL,
	}
}

// RegisterRoutes mounts the login contract handler on mux.
// The route is wrapped with two middlewares:
//  1. httpcookie.Middleware (outermost) — intercepts WriteHeader to emit the
//     Set-Cookie: gocell_rt header on 2xx responses when the adapter called
//     httpcookie.SetRefresh.
//  2. injectLoginTenant (innermost) — reads X-Tenant-ID from the HTTP request
//     headers and stores it in ctx under loginTenantCtxKey so LoginAdapter.Login
//     can retrieve it without access to the raw request.
//
// The cookie middleware must be outermost so its response-writer wrapper sees
// WriteHeader before the underlying writer commits the status.
//
// cellmw.NewHeaderInjectMux is used instead of a local wrapper struct so that
// DeclareHTTPContract (which names contractspec.ContractSpec) stays in
// runtime/ — cells/ must not import kernel/contractspec directly
// (archtest CELLS-NO-CONTRACTSPEC-IMPORT-01).
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	wrap := func(next http.Handler) http.Handler {
		return httpcookie.Middleware(h.cookieTTL)(injectLoginTenant(next))
	}
	return h.loginH.RegisterRoutes(cellmw.NewHeaderInjectMux(mux, wrap))
}

// injectLoginTenant is the per-request middleware that reads X-Tenant-ID from
// the HTTP header and stores it in ctx under loginTenantCtxKey.
func injectLoginTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), loginTenantCtxKey{}, r.Header.Get(headerTenantID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
