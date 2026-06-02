package sessionlogin

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	logingen "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/contractspec"
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
// value into ctx before dispatching to LoginAdapter.Login.
type Handler struct {
	loginH *logingen.Handler
}

// NewHandler creates a sessionlogin Handler using the generated login handler.
// No policy argument: the login endpoint is Public (no JWT required).
func NewHandler(svc *Service) *Handler {
	return &Handler{
		loginH: logingen.NewHandler(LoginAdapter{svc}),
	}
}

// RegisterRoutes mounts the login contract handler on mux.
// The route is wrapped with a thin middleware that reads X-Tenant-ID from the
// HTTP request headers and stores it in ctx under loginTenantCtxKey so
// LoginAdapter.Login can retrieve it without access to the raw request.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	// Delegate to the generated handler's RegisterRoutes, but wrap the mux so that
	// every handler registered via auth.Mount is wrapped with the X-Tenant-ID
	// header injection middleware. tenantInjectMux intercepts Handle calls and
	// wraps each http.Handler to inject the header value into ctx.
	return h.loginH.RegisterRoutes(&tenantInjectMux{mux: mux})
}

// tenantInjectMux is a cell.RouteHandler wrapper that injects a middleware
// around any registered http.Handler to forward X-Tenant-ID into ctx.
type tenantInjectMux struct {
	mux kcell.RouteHandler
}

func (m *tenantInjectMux) Handle(pattern string, handler http.Handler) {
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), loginTenantCtxKey{}, r.Header.Get(headerTenantID))
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	m.mux.Handle(pattern, wrapped)
}

// Prefix delegates to the inner mux if it implements the Prefixer interface
// (used by auth.Mount to compute the chi-relative registration path).
func (m *tenantInjectMux) Prefix() string {
	if p, ok := m.mux.(kcell.Prefixer); ok {
		return p.Prefix()
	}
	return ""
}

// DeclareAuthMeta forwards auth-route metadata to the inner mux. auth.Mount
// type-asserts the mux to cell.AuthRouteDeclarer to push each route's auth
// attributes; without this forwarder the wrapper would swallow the assertion
// and the router's policy-coverage check would flag the login route as
// "registered without auth.Mount".
func (m *tenantInjectMux) DeclareAuthMeta(meta kcell.AuthRouteMeta) error {
	if d, ok := m.mux.(kcell.AuthRouteDeclarer); ok {
		return d.DeclareAuthMeta(meta)
	}
	return nil
}

// DeclareHTTPContract forwards the route's ContractSpec to the inner mux for
// the same reason as DeclareAuthMeta (auth.Mount type-asserts to
// cell.HTTPContractDeclarer).
func (m *tenantInjectMux) DeclareHTTPContract(spec contractspec.ContractSpec) error {
	if d, ok := m.mux.(kcell.HTTPContractDeclarer); ok {
		return d.DeclareHTTPContract(spec)
	}
	return nil
}
