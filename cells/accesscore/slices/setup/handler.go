package setup

import (
	"context"

	adminGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/admin/v1"
	statusGen "github.com/ghbvf/gocell/generated/contracts/http/auth/setup/status/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
)

// StatusAdapter implements statusGen.Service for http.auth.setup.status.v1.
// Both setup endpoints are Public (no JWT required): no admin exists yet during
// first-run bootstrap.
type StatusAdapter struct{ S *Service }

// Status implements statusGen.Service. The generated handler already decodes
// the (empty) request.
func (a StatusAdapter) Status(ctx context.Context, _ *statusGen.Request) (*statusGen.Response, error) {
	out, err := a.S.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &statusGen.Response{
		Data: &statusGen.ResponseData{HasAdmin: out.HasAdmin},
	}, nil
}

// AdminAdapter implements adminGen.Service for http.auth.setup.admin.v1.
type AdminAdapter struct{ S *Service }

// Admin implements adminGen.Service. The generated handler validates and
// decodes username+email+password from the request body.
func (a AdminAdapter) Admin(ctx context.Context, req *adminGen.Request) (*adminGen.Response, error) {
	out, err := a.S.CreateAdmin(ctx, CreateAdminInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		return nil, err
	}
	return &adminGen.Response{
		Data: &adminGen.ResponseData{
			ID:        out.ID,
			Username:  out.Username,
			Email:     out.Email,
			CreatedAt: out.CreatedAt,
		},
	}, nil
}

// Handler exposes the setup endpoints over HTTP.
// Both endpoints are Public: no JWT required (first-run bootstrap scenario).
type Handler struct {
	statusH *statusGen.Handler
	adminH  *adminGen.Handler
}

// NewHandler creates a setup Handler using the generated status and admin handlers.
// No policy arguments: both endpoints are Public (auth.Route{Public: true} baked in).
func NewHandler(svc *Service) *Handler {
	return &Handler{
		statusH: statusGen.NewHandler(StatusAdapter{svc}),
		adminH:  adminGen.NewHandler(AdminAdapter{svc}),
	}
}

// RegisterRoutes mounts the setup contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) error {
	if err := h.statusH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.adminH.RegisterRoutes(mux)
}
