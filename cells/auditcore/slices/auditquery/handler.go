package auditquery

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	cell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// specAuditList — cross-checked against contracts/http/audit/list/v1/contract.yaml.
var specAuditList = wrapper.ContractSpec{
	ID: "http.audit.list.v1", Kind: "http", Transport: "http",
	Method: "GET", Path: "/api/v1/audit/entries",
}

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
	if e == nil {
		return AuditEntryResponse{}
	}
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

// auditQueryPolicy permits the request when:
//   - actorId query param is empty or equals authenticated subject (self-access)
//   - OR subject has the "admin" role
//
// SelfOr cannot be used here because "self" is determined by the actorId query
// parameter, not a path parameter.
// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01): role-name literal is migrated to permission-based authz when that backlog item lands.
func auditQueryPolicy(r *http.Request) error {
	ctx := r.Context()
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
	}
	actorID := r.URL.Query().Get("actorId")
	if actorID == "" || actorID == p.Subject {
		return nil
	}
	return auth.AnyRole(dto.RoleAdmin)(r)
}

// RegisterRoutes registers auditquery routes with the audit-query policy
// via auth.Mount so every request emits a contract-tagged span.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specAuditList,
		Handler:  http.HandlerFunc(h.HandleQuery),
		Policy:   auditQueryPolicy,
	})
}

// HandleQuery handles GET /api/v1/audit/entries.
// Query parameters: eventType, actorId, from, to (RFC3339), limit, cursor.
//
// Trust boundary: non-admin users can only query their own audit entries.
// If actorId is omitted, it defaults to the authenticated subject.
// If actorId differs from subject, admin role is required.
// Policy is enforced by auditQueryPolicy (see above).
func (h *Handler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	// auditQueryPolicy (declared at route registration) guarantees subject presence.
	// Guard defensively: if policy is misconfigured and auth middleware didn't run,
	// fail closed rather than panic on nil dereference.
	p, ok := auth.FromContext(r.Context())
	if !ok {
		slog.Error("audit: handler reached without principal — policy chain may be misconfigured",
			slog.String("path", r.URL.Path),
			slog.String("method", r.Method),
		)
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, string(errcode.ErrAuthUnauthorized), "authentication required")
		return
	}
	subject := p.Subject

	actorID := r.URL.Query().Get("actorId")
	if actorID == "" {
		actorID = subject
	}
	if actorID != subject {
		slog.Info("audit: admin querying other user",
			slog.String("admin", subject),
			slog.String("target_actor", actorID),
		)
	}

	filters := ports.AuditFilters{
		EventType: r.URL.Query().Get("eventType"),
		ActorID:   actorID,
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

	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
		return
	}

	result, err := h.svc.Query(r.Context(), filters, pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toAuditEntryResponse))
}
