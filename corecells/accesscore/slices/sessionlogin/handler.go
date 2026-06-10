package sessionlogin

import (
	"context"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/httpcookie"
	logingen "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/cellmw"
)

// LoginAdapter implements logingen.Service for http.auth.login.v1.
// It adapts the slice-internal Service (Login takes LoginInput) to the
// generated interface (Login takes *logingen.Request). The tenant ID arrives on
// the generated Request as XTenantID, populated by the generated handler from
// the X-Tenant-ID request header declared in contract.yaml endpoints.http.headers.
type LoginAdapter struct{ S *Service }

// Login implements logingen.Service. The generated handler validates and decodes
// username+password from the request body and populates req.XTenantID from the
// X-Tenant-ID header (populate-only — no codegen gate). The tenant header drives
// a two-stage validation inside Service.Login:
//
//   - Absent header (empty string): validation.RequireNotEmpty rejects with 400
//     (ErrAuthLoginInvalidInput) before any authentication work is attempted.
//   - Present but malformed UUID: tenant.ParseTenantID rejects with a uniform 401
//     (ErrAuthLoginFailed), the same shape as wrong-password, to prevent tenant
//     enumeration (ADR 1160).
func (a LoginAdapter) Login(ctx context.Context, req *logingen.Request) (logingen.LoginResponseObject, error) {
	pair, err := a.S.Login(ctx, LoginInput{
		Username: req.Username,
		Password: req.Password,
		TenantID: req.XTenantID,
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
// Handler wraps the generated logingen.Handler with httpcookie.Middleware to emit
// the refresh token as an httpOnly cookie on 201 responses (BR-005, #1278). The
// X-Tenant-ID header is consumed by the generated handler (Request.XTenantID), so
// no tenant-injection middleware is needed.
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

// RegisterRoutes mounts the login contract handler on mux, wrapped with
// httpcookie.Middleware: it intercepts WriteHeader to emit the
// Set-Cookie: __Host-gocell_rt header on 2xx responses when the adapter called
// httpcookie.SetRefresh.
//
// cellmw.NewHeaderInjectMux is used instead of a local wrapper struct so that
// DeclareHTTPContract (which names contractspec.ContractSpec) stays in
// runtime/ — cells/ must not import kernel/contractspec directly
// (archtest CELLS-NO-CONTRACTSPEC-IMPORT-01).
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	wrap := func(next http.Handler) http.Handler {
		return httpcookie.Middleware(h.cookieTTL)(next)
	}
	return h.loginH.RegisterRoutes(cellmw.NewHeaderInjectMux(mux, wrap))
}
