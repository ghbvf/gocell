package sessionlogout

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for session logout.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-logout Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleLogout handles DELETE /api/v1/access/sessions/{id}.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if err := h.svc.Logout(r.Context(), sessionID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
