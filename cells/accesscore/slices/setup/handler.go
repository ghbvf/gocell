package setup

import (
	"context"
	"net/http"

	adminGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/admin/v1"
	statusGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/status/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
)

// StatusAdapter implements statusGen.Service for http.auth.setup.status.v1.
// The status endpoint is always Public (no JWT required): no admin exists yet
// during first-run bootstrap.
type StatusAdapter struct{ S *Service }

// Status implements statusGen.Service. The generated handler already decodes
// the (empty) request.
func (a StatusAdapter) Status(ctx context.Context, _ *statusGen.Request) (statusGen.StatusResponseObject, error) {
	out, err := a.S.Status(ctx)
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
// decodes username+email+password from the request body.
func (a AdminAdapter) Admin(ctx context.Context, req *adminGen.Request) (adminGen.AdminResponseObject, error) {
	out, err := a.S.CreateAdmin(ctx, CreateAdminInput{
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

// RegisterRoutes mounts the setup contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.statusH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.adminH.RegisterRoutes(mux)
}
