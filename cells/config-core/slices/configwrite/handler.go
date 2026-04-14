package configwrite

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// ConfigEntryResponse is the public DTO for ConfigEntry, isolating the API
// contract from the domain model.
type ConfigEntryResponse struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toConfigEntryResponse(e *domain.ConfigEntry) ConfigEntryResponse {
	return ConfigEntryResponse{
		ID: e.ID, Key: e.Key, Value: e.Value, Version: e.Version,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

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

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toConfigEntryResponse(entry)})
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

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toConfigEntryResponse(entry)})
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
