package setup

import (
	"context"
	"fmt"
	"net/http"

	adminGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/admin/v1"
	statusGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/status/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/projection"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// StatusAdapter implements statusGen.Service for http.auth.setup.status.v1.
// The status endpoint is always Public (no JWT required): no admin exists yet
// during first-run bootstrap.
type StatusAdapter struct{ S *Service }

// Status implements statusGen.Service. The generated handler populates
// req.XTenantID from the X-Tenant-ID header declared in contract.yaml
// endpoints.http.headers; this adapter parses it to scope the admin existence
// check to the tenant.
func (a StatusAdapter) Status(ctx context.Context, req *statusGen.Request) (statusGen.StatusResponseObject, error) {
	tid, err := tenant.ParseTenantID(req.XTenantID)
	if err != nil {
		// Missing or malformed tenant → report HasAdmin:false rather than surfacing
		// the parse error. This pre-auth bootstrap probe must stay non-enumerable:
		// an invalid/absent X-Tenant-ID must not let a caller distinguish tenants.
		// This fail-soft DELIBERATELY supersedes the fail-closed reading of review
		// F3 — see ADR 1160 §"setup/status fail-soft ... supersedes ... (F3)".
		return toStatusProjection(false)
	}
	out, err := a.S.Status(ctx, tid)
	if err != nil {
		return nil, err
	}
	return toStatusProjection(out.HasAdmin)
}

// toStatusProjection wraps a HasAdmin boolean into the sealed projection envelope.
// Identity projection (epic #1337 PR-12); masking obligation source becomes the ABAC Decision in PR-10.
func toStatusProjection(hasAdmin bool) (statusGen.StatusResponseObject, error) {
	data, err := projection.NewProjection(authz.IdentityFieldMask(), statusGen.ResponseData{HasAdmin: hasAdmin}.ToMap())
	if err != nil {
		return nil, err
	}
	return statusGen.Status200JSONResponse{Data: data}, nil
}

// errMsgTenantIDRequired is the error message for a missing or malformed
// X-Tenant-ID header on the setup admin endpoint.
// Must be a const literal (MESSAGE-CONST-LITERAL-01).
const errMsgTenantIDRequired = "tenantId is required and must be a valid UUID"

// AdminAdapter implements adminGen.Service for http.auth.setup.admin.v1.
type AdminAdapter struct{ S *Service }

// Admin implements adminGen.Service. The generated handler validates and decodes
// username+email+password from the request body and populates req.XTenantID from
// the X-Tenant-ID header. Tenant parse and 400 mapping live here: an empty or
// malformed X-Tenant-ID header causes tenant.ParseTenantID to fail, which this
// adapter maps to 400 ERR_AUTH_IDENTITY_INVALID_INPUT (ADR 1160). The typed
// tenant.TenantID is then passed to Service.CreateAdmin, which trusts it.
func (a AdminAdapter) Admin(ctx context.Context, req *adminGen.Request) (adminGen.AdminResponseObject, error) {
	tid, perr := tenant.ParseTenantID(req.XTenantID)
	if perr != nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			errMsgTenantIDRequired,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("invalid tenantId: %v", perr))))
	}
	out, err := a.S.CreateAdmin(ctx, CreateAdminInput{
		TenantID: tid,
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

// RegisterRoutes mounts the setup contract handlers on mux. The X-Tenant-ID
// header is consumed by the generated handlers (Request.XTenantID), so no
// tenant-injection middleware is needed.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.statusH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.adminH.RegisterRoutes(mux)
}
