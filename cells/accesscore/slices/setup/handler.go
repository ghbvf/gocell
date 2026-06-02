package setup

import (
	"context"
	"net/http"

	adminGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/admin/v1"
	statusGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/status/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/http/cellmw"
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
//
// cellmw.NewHeaderInjectMux is used instead of a local wrapper struct so that
// DeclareHTTPContract (which names contractspec.ContractSpec) stays in
// runtime/ — cells/ must not import kernel/contractspec directly
// (archtest CELLS-NO-CONTRACTSPEC-IMPORT-01).
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	wrapped := cellmw.NewHeaderInjectMux(mux, injectSetupTenant)
	if err := h.statusH.RegisterRoutes(wrapped); err != nil {
		return err
	}
	return h.adminH.RegisterRoutes(wrapped)
}

// injectSetupTenant is the per-request middleware that reads X-Tenant-ID from
// the HTTP header and stores it in ctx under setupTenantCtxKey.
func injectSetupTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), setupTenantCtxKey{}, r.Header.Get(headerTenantID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
