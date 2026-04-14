package configpublish

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

// ConfigVersionResponse is the public DTO for ConfigVersion.
type ConfigVersionResponse struct {
	ID          string     `json:"id"`
	ConfigID    string     `json:"configId"`
	Version     int        `json:"version"`
	Value       string     `json:"value"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

func toConfigVersionResponse(v *domain.ConfigVersion) ConfigVersionResponse {
	return ConfigVersionResponse{
		ID: v.ID, ConfigID: v.ConfigID, Version: v.Version,
		Value: v.Value, PublishedAt: v.PublishedAt,
	}
}

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
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toConfigVersionResponse(version)})
}

// HandleRollback handles POST /{key}/rollback — rolls back a config entry.
func (h *Handler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Version int `json:"version"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Rollback(r.Context(), key, req.Version)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toConfigEntryResponse(entry)})
}
