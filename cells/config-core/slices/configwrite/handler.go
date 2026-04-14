package configwrite

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for config write operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-write Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleCreate handles POST / — creates a new config entry.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Create(r.Context(), CreateInput{Key: req.Key, Value: req.Value})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
}

// HandleUpdate handles PUT /{key} — updates an existing config entry.
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Value string `json:"value"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Update(r.Context(), UpdateInput{Key: key, Value: req.Value})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
}

// HandleDelete handles DELETE /{key} — deletes a config entry.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	if err := h.svc.Delete(r.Context(), key); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
