package setup

import (
	"context"
	"net/http"

	adminGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/admin/v1"
	statusGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/status/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// headerTenantID is the HTTP header name for the tenant identifier on
// bootstrap endpoints (pre-auth, no JWT claim available).
const headerTenantID = "X-Tenant-ID"

// setupTenantCtxKey is the unexported context key used to ferry the
// X-Tenant-ID header value from the HTTP request into the adapter methods.
type setupTenantCtxKey struct{}

// StatusAdapter implements statusGen.Service for http.auth.setup.status.v1.
// The status endpoint is always Public (no JWT required): no admin exists yet
// during first-run bootstrap.
type StatusAdapter struct{ S *Service }

// Status implements statusGen.Service. Reads X-Tenant-ID from ctx (injected
// by the wrapping mux) to scope the admin existence check to the tenant.
func (a StatusAdapter) Status(ctx context.Context, _ *statusGen.Request) (statusGen.StatusResponseObject, error) {
	rawTID, _ := ctx.Value(setupTenantCtxKey{}).(string)
	tid, err := tenant.ParseTenantID(rawTID)
	if err != nil {
		// Missing or malformed tenant → report HasAdmin:false rather than surfacing
		// the parse error. This pre-auth bootstrap probe must stay non-enumerable:
		// an invalid/absent X-Tenant-ID must not let a caller distinguish tenants.
		//nolint:nilerr // intentional fail-soft: invalid tenant ⇒ non-enumerable false
		return statusGen.Status200JSONResponse{
			Data: &statusGen.ResponseData{HasAdmin: false},
		}, nil
	}
	out, err := a.S.Status(ctx, tid)
	if err != nil {
		return nil, err
	}
	return statusGen.Status200JSONResponse{
		Data: &statusGen.ResponseData{HasAdmin: out.HasAdmin},
	}, nil
}

// AdminAdapter implements adminGen.Service for http.auth.setup.admin.v1.
type AdminAdapter struct{ S *Service }

// Admin implements adminGen.Service. The generated handler validates and
// decodes username+email+password from the request body. TenantID is read
// from ctx (injected by the wrapping mux from X-Tenant-ID header).
func (a AdminAdapter) Admin(ctx context.Context, req *adminGen.Request) (adminGen.AdminResponseObject, error) {
	rawTID, _ := ctx.Value(setupTenantCtxKey{}).(string)
	out, err := a.S.CreateAdmin(ctx, CreateAdminInput{
		TenantID: rawTID,
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		return nil, err
	}
	return adminGen.Admin201JSONResponse{
		Data: &adminGen.ResponseData{
			ID:        out.ID,
			Username:  out.Username,
			Email:     out.Email,
			CreatedAt: out.CreatedAt,
		},
	}, nil
}

// Handler exposes the setup endpoints over HTTP.
//
// The status endpoint is always Public (no admin exists yet during first-run).
// The admin endpoint is Bootstrap (HTTP Basic Auth via env credentials); the
// bootstrapAuth middleware is mandatory and is threaded straight to the
// generated handler — see ADR §D1 (auth.bootstrap is a closed contract: no
// "declared bootstrap but no auth wired" intermediate state).
type Handler struct {
	statusH *statusGen.Handler
	adminH  *adminGen.Handler
}

// NewHandler creates a setup Handler.
//
// bootstrapAuth is REQUIRED — it is the per-route replacement authentication
// (typically runtime/auth.NewBootstrapMiddleware wired by the composition
// root). The generated admin handler panics at construction if bootstrapAuth
// is nil; this constructor enforces the same invariant up front so the failure
// mode is "Cell.Init returns a clear error" rather than "process panic deep
// in the generated layer".
func NewHandler(svc *Service, bootstrapAuth func(http.Handler) http.Handler) *Handler {
	return &Handler{
		statusH: statusGen.NewHandler(StatusAdapter{svc}),
		adminH:  adminGen.NewHandler(AdminAdapter{svc}, bootstrapAuth),
	}
}

// RegisterRoutes mounts the setup contract handlers on mux. Both handlers are
// wrapped with a thin middleware that reads X-Tenant-ID from the HTTP request
// headers and stores it in ctx under setupTenantCtxKey.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.statusH.RegisterRoutes(&setupTenantInjectMux{mux: mux}); err != nil {
		return err
	}
	return h.adminH.RegisterRoutes(&setupTenantInjectMux{mux: mux})
}

// setupTenantInjectMux is a cell.RouteHandler wrapper that injects a middleware
// around any registered http.Handler to forward X-Tenant-ID into ctx.
type setupTenantInjectMux struct {
	mux kcell.RouteHandler
}

func (m *setupTenantInjectMux) Handle(pattern string, handler http.Handler) {
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), setupTenantCtxKey{}, r.Header.Get(headerTenantID))
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	m.mux.Handle(pattern, wrapped)
}

// Prefix delegates to the inner mux if it implements the Prefixer interface.
func (m *setupTenantInjectMux) Prefix() string {
	if p, ok := m.mux.(kcell.Prefixer); ok {
		return p.Prefix()
	}
	return ""
}

// DeclareAuthMeta forwards auth-route metadata to the inner mux so auth.Mount's
// cell.AuthRouteDeclarer assertion succeeds through the wrapper (otherwise the
// router policy-coverage check flags the setup routes as registered without
// auth.Mount).
func (m *setupTenantInjectMux) DeclareAuthMeta(meta kcell.AuthRouteMeta) error {
	if d, ok := m.mux.(kcell.AuthRouteDeclarer); ok {
		return d.DeclareAuthMeta(meta)
	}
	return nil
}

// DeclareHTTPContract forwards the route's ContractSpec to the inner mux
// (auth.Mount type-asserts to cell.HTTPContractDeclarer).
func (m *setupTenantInjectMux) DeclareHTTPContract(spec contractspec.ContractSpec) error {
	if d, ok := m.mux.(kcell.HTTPContractDeclarer); ok {
		return d.DeclareHTTPContract(spec)
	}
	return nil
}
