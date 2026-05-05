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
// The status endpoint is always Public. The admin endpoint is Protected with
// Bootstrap auth when bootstrapMiddleware is set via WithAdminMiddleware; it is
// Public otherwise (first-run interactive without env credentials).
type Handler struct {
	statusH             *statusGen.Handler
	adminH              *adminGen.Handler
	bootstrapMiddleware func(http.Handler) http.Handler
}

// HandlerOption configures a Handler.
type HandlerOption func(*Handler)

// WithAdminMiddleware injects an HTTP middleware that wraps the admin creation
// endpoint. In interactive bootstrap mode the composition root passes the
// bootstrap-auth middleware (Basic Auth credential check); the status endpoint
// is not wrapped.
func WithAdminMiddleware(mw func(http.Handler) http.Handler) HandlerOption {
	return func(h *Handler) { h.bootstrapMiddleware = mw }
}

// NewHandler creates a setup Handler using the generated status and admin handlers.
// By default the admin endpoint is Public. Pass WithAdminMiddleware to enforce
// authentication on it (e.g. interactive bootstrap mode).
func NewHandler(svc *Service, opts ...HandlerOption) *Handler {
	h := &Handler{
		statusH: statusGen.NewHandler(StatusAdapter{svc}),
		adminH:  adminGen.NewHandler(AdminAdapter{svc}),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterRoutes mounts the setup contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) error {
	if err := h.statusH.RegisterRoutes(mux); err != nil {
		return err
	}
	if h.bootstrapMiddleware != nil {
		// Wrap the admin handler by routing through a middlewareRouteMux that
		// applies the bootstrap middleware around the registered handler. This
		// avoids a manual wrapper.ContractSpec{} literal (prohibited in cells/
		// by NO-MANUAL-CONTRACTSPEC-LITERAL-01); the generated RegisterRoutes
		// owns the ContractSpec declaration.
		return h.adminH.RegisterRoutes(newMiddlewareRouteMux(mux, h.bootstrapMiddleware))
	}
	return h.adminH.RegisterRoutes(mux)
}

// middlewareRouteMux wraps a kcell.RouteMux and applies a middleware to every
// handler registered via Handle, so that the bootstrap credential check is
// enforced around the generated handler without re-declaring the ContractSpec.
// Only Handle calls are intercepted; all optional mux interfaces (Prefixer,
// AuthRouteDeclarer, HTTPContractDeclarer) are delegated via type assertion to
// preserve auth metadata registration.
type middlewareRouteMux struct {
	kcell.RouteMux
	mw func(http.Handler) http.Handler
}

func newMiddlewareRouteMux(mux kcell.RouteMux, mw func(http.Handler) http.Handler) *middlewareRouteMux {
	return &middlewareRouteMux{RouteMux: mux, mw: mw}
}

// Handle wraps the provided handler with the middleware before delegating.
func (m *middlewareRouteMux) Handle(pattern string, handler http.Handler) {
	m.RouteMux.Handle(pattern, m.mw(handler))
}

// Prefix delegates to the underlying mux if it implements cell.Prefixer.
func (m *middlewareRouteMux) Prefix() string {
	if p, ok := m.RouteMux.(interface{ Prefix() string }); ok {
		return p.Prefix()
	}
	return ""
}

// DeclareAuthMeta delegates to the underlying mux if it implements
// cell.AuthRouteDeclarer, so that Bootstrap:true metadata is recorded.
func (m *middlewareRouteMux) DeclareAuthMeta(meta kcell.AuthRouteMeta) error {
	if d, ok := m.RouteMux.(kcell.AuthRouteDeclarer); ok {
		return d.DeclareAuthMeta(meta)
	}
	return nil
}
