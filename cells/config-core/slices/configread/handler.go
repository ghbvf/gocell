package configread

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
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
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toConfigEntryResponse(entry)})
}

// HandleList handles GET /?limit=N&cursor=TOKEN — returns paginated config entries.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	pageReq, err := httputil.ParsePageRequest(r)
	if err != nil {
		slog.Warn("pagination: request validation failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path),
		)
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	result, err := h.svc.List(r.Context(), pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toConfigEntryResponse))
}
