package auditquery

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
)

// AuditEntryResponse is the public DTO for AuditEntry, excluding internal
// hash-chain integrity fields (PrevHash, Hash) that are implementation details.
// Payload is preserved as it contains the audited operation content.
type AuditEntryResponse struct {
	ID        string          `json:"id"`
	EventID   string          `json:"eventId"`
	EventType string          `json:"eventType"`
	ActorID   string          `json:"actorId"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func toAuditEntryResponse(e *domain.AuditEntry) AuditEntryResponse {
	return AuditEntryResponse{
		ID: e.ID, EventID: e.EventID, EventType: e.EventType,
		ActorID: e.ActorID, Timestamp: e.Timestamp, Payload: e.Payload,
	}
}

// Handler provides HTTP endpoints for audit queries.
type Handler struct {
	svc *Service
}

// NewHandler creates an audit-query Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleQuery handles GET /api/v1/audit/entries.
// Query parameters: eventType, actorId, from, to (RFC3339), limit, cursor.
func (h *Handler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	filters := ports.AuditFilters{
		EventType: r.URL.Query().Get("eventType"),
		ActorID:   r.URL.Query().Get("actorId"),
	}

	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		t, err := time.Parse(time.RFC3339Nano, fromStr)
		if err != nil {
			httputil.WriteError(r.Context(), w, http.StatusBadRequest, string(errcode.ErrInvalidTimeFormat),
				"invalid 'from' parameter: expected RFC3339 format")
			return
		}
		filters.From = t
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		t, err := time.Parse(time.RFC3339Nano, toStr)
		if err != nil {
			httputil.WriteError(r.Context(), w, http.StatusBadRequest, string(errcode.ErrInvalidTimeFormat),
				"invalid 'to' parameter: expected RFC3339 format")
			return
		}
		filters.To = t
	}

	pageReq, err := httputil.ParsePageRequest(r)
	if err != nil {
		slog.Warn("pagination: request validation failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path),
		)
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	result, err := h.svc.Query(r.Context(), filters, pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toAuditEntryResponse))
}
