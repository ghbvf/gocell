package auditquery

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/httputil"
)

const (
	errInvalidTimeFormat = "ERR_VALIDATION_INVALID_TIME_FORMAT"
)

// Handler provides HTTP endpoints for audit queries.
type Handler struct {
	svc *Service
}

// NewHandler creates an audit-query Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleQuery handles GET /api/v1/audit/entries.
// Query parameters: eventType, actorId, from, to (RFC3339).
func (h *Handler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	filters := ports.AuditFilters{
		EventType: r.URL.Query().Get("eventType"),
		ActorID:   r.URL.Query().Get("actorId"),
	}

	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, errInvalidTimeFormat,
				"invalid 'from' parameter: expected RFC3339 format")
			return
		}
		filters.From = t
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, errInvalidTimeFormat,
				"invalid 'to' parameter: expected RFC3339 format")
			return
		}
		filters.To = t
	}

	entries, err := h.svc.Query(r.Context(), filters)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data":  entries,
		"total": len(entries),
	})
}
