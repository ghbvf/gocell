package configpublish

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for config publish operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-publish Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes returns a chi.Router with config-publish routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/{key}/publish", h.handlePublish)
	r.Post("/{key}/rollback", h.handleRollback)
	return r
}

func (h *Handler) handlePublish(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	version, err := h.svc.Publish(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": version})
}

func (h *Handler) handleRollback(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	var req struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "ERR_VALIDATION_REQUIRED_FIELD", "invalid request body")
		return
	}

	entry, err := h.svc.Rollback(r.Context(), key, req.Version)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": entry})
}
