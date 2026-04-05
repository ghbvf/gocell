package auditquery

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/httputil"
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
// Query parameters: event_type, actor_id, from, to (RFC3339).
func (h *Handler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	filters := ports.AuditFilters{
		EventType: r.URL.Query().Get("event_type"),
		ActorID:   r.URL.Query().Get("actor_id"),
	}

	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			filters.From = t
		}
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			filters.To = t
		}
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
