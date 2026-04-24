package setup

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler exposes the setup endpoints over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler creates a setup Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleStatus handles GET /api/v1/setup/status.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.Status(r.Context())
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// HandleCreateAdmin handles POST /api/v1/setup/admin.
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
