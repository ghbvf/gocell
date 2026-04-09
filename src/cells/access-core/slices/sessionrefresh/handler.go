package sessionrefresh

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for session refresh.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-refresh Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleRefresh handles POST /api/v1/access/sessions/refresh.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteDecodeError(w, err)
		return
	}

	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": pair})
}
