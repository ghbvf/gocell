package sessionlogin

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

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
	if err := httputil.DecodeJSON(r, &req); err != nil {
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

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": pair})
}
