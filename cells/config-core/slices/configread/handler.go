package configread

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
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
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
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

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, dto.ToConfigEntryResponse))
}
