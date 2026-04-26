package sessionlogin

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// specLogin declares the contract for the session-login endpoint, cross-checked
// against contracts/http/auth/login/v1/contract.yaml by FMT-18.
var specLogin = wrapper.ContractSpec{
	ID: "http.auth.login.v1", Kind: "http", Transport: "http",
	Method: "POST", Path: "/api/v1/access/sessions/login",
}

// Handler provides HTTP endpoints for session login.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-login Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleLogin handles POST /api/v1/access/sessions/login.
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	pair, err := h.svc.Login(r.Context(), LoginInput{
		Username: req.Username, Password: req.Password,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": dto.ToTokenPairResponse(pair)})
}

// RegisterRoutes registers the session-login route on mux. Login is a public
// endpoint (no JWT required): callers identify themselves via username+password.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.MustMount(mux, auth.Route{
		Contract: specLogin,
		Handler:  http.HandlerFunc(h.HandleLogin),
		Public:   true,
	})
}
