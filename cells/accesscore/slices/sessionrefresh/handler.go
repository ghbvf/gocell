package sessionrefresh

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// specRefresh declares the contract for the session-refresh endpoint,
// cross-checked against contracts/http/auth/refresh/v1/contract.yaml by FMT-18.
var specRefresh = wrapper.ContractSpec{
	ID: "http.auth.refresh.v1", Kind: "http", Transport: "http",
	Method: "POST", Path: "/api/v1/access/sessions/refresh",
}

// Handler provides HTTP endpoints for session refresh.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-refresh Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers the session-refresh route on mux via auth.Mount so
// CH-04/CH-05 governance can correlate this contract to HandleRefresh.
// Refresh is a public endpoint: callers supply a refresh token in the request
// body; no JWT is required.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := auth.Mount(mux, auth.Route{
		Contract: specRefresh,
		Handler:  http.HandlerFunc(h.HandleRefresh),
		Public:   true,
	}); err != nil {
		return err
	}
	return nil
}

// HandleRefresh handles POST /api/v1/access/sessions/refresh.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := httputil.DecodeJSONStrict(r, &req, httputil.DefaultDecodeJSONLimit); err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}

	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToTokenPairResponse(pair)})
}
