package configpublish

import (
	"encoding/json"
	"net/http"

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

// HandlePublish handles POST /{key}/publish — publishes a config entry.
func (h *Handler) HandlePublish(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	version, err := h.svc.Publish(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": version})
}

// HandleRollback handles POST /{key}/rollback — rolls back a config entry.
func (h *Handler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

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
