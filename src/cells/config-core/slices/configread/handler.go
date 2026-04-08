package configread

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for config read operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-read Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleGet handles GET /{key} — returns a single config entry.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	entry, err := h.svc.GetByKey(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": entry})
}

// HandleList handles GET / — returns all config entries.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	entries, err := h.svc.List(r.Context())
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": entries, "total": len(entries)})
}
