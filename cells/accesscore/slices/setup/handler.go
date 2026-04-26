package setup

import (
	"net/http"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// specSetupStatus and specSetupAdmin declare the contracts for setup endpoints,
// cross-checked against contracts/http/auth/setup/*/v1/contract.yaml by FMT-18.
var (
	specSetupStatus = wrapper.ContractSpec{
		ID: "http.auth.setup.status.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/access/setup/status",
	}
	specSetupAdmin = wrapper.ContractSpec{
		ID: "http.auth.setup.admin.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/access/setup/admin",
	}
)

// Handler exposes the setup endpoints over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler creates a setup Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers setup routes on mux via auth.Mount so CH-04/CH-05
// governance can correlate contracts to handler functions.
//
// Both endpoints are Public: no admin exists yet to authenticate against
// during first-run setup. Once an admin exists, CreateAdmin returns 410 Gone
// via a fast-path Status check before bcrypt runs.
//
// The /setup tree is mounted under the cell's /access prefix (Consul
// /acl/bootstrap convention rather than Vault's top-level /sys/init) so the
// path matches Cell ownership.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specSetupStatus,
		Handler:  http.HandlerFunc(h.HandleStatus),
		Public:   true,
	})
	auth.Mount(mux, auth.Route{
		Contract: specSetupAdmin,
		Handler:  http.HandlerFunc(h.HandleCreateAdmin),
		Public:   true,
	})
}

// HandleStatus handles GET /api/v1/access/setup/status.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.Status(r.Context())
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// HandleCreateAdmin handles POST /api/v1/access/setup/admin.
func (h *Handler) HandleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	out, err := h.svc.CreateAdmin(r.Context(), CreateAdminInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": out})
}
