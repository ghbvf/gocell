package auditquery

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	auditlist "github.com/ghbvf/gocell/generated/contracts/http/audit/list/v1"
	cell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
)

// AuditEntryResponse is the public DTO for ledger.Entry, excluding internal
// hash-chain integrity fields (PrevHash, Hash) that are implementation details.
// Payload is redacted of sensitive fields (B2-C-09) before being returned.
type AuditEntryResponse struct {
	ID        string          `json:"id"`
	EventID   string          `json:"eventId"`
	EventType string          `json:"eventType"`
	ActorID   string          `json:"actorId"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func toAuditEntryResponse(e *ledger.Entry) AuditEntryResponse {
	if e == nil {
		return AuditEntryResponse{}
	}
	return AuditEntryResponse{
		ID:        e.ID,
		EventID:   e.EventID,
		EventType: e.EventType,
		ActorID:   e.ActorID,
		Timestamp: e.Timestamp,
		Payload:   e.Payload,
	}
}

// auditQueryPolicy permits the request when:
//   - actorId query param is empty or equals authenticated subject (self-access)
//   - OR subject has the "admin" role
//
// SelfOr cannot be used here because "self" is determined by the actorId query
// parameter, not a path parameter.
// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01): role-name literal is migrated to
// permission-based authz when that backlog item lands.
func auditQueryPolicy(r *http.Request) error {
	ctx := r.Context()
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	actorID := r.URL.Query().Get("actorId")
	if actorID == "" || actorID == p.Subject {
		return nil
	}
	return auth.AnyRole(auth.RoleAdmin)(r)
}

// ListAdapter wraps Service to implement auditlist.Service for http.audit.list.v1.
// It handles actor defaulting to subject, time parsing, and pagination mapping.
type ListAdapter struct {
	S *Service
}

// List implements auditlist.Service. The request fields (actorId, from, to, limit,
// cursor, eventType) are already decoded and basic-validated by handler_gen.
// B2-C-09: Payload is redacted of sensitive fields before returning to client.
func (a ListAdapter) List(ctx context.Context, req *auditlist.Request) (auditlist.ListResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	subject := p.Subject

	actorID := req.ActorId
	if actorID == "" {
		actorID = subject
	}
	if actorID != subject {
		slog.Info("audit: admin querying other user",
			slog.String("admin", subject),
			slog.String("target_actor", actorID),
		)
	}

	filters := ledger.AuditFilters{
		EventType: req.EventType,
		ActorID:   actorID,
	}

	if req.From != "" {
		t, err := time.Parse(time.RFC3339Nano, req.From)
		if err != nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrInvalidTimeFormat,
				"invalid 'from' parameter: expected RFC3339 format")
		}
		filters.From = t
	}
	if req.To != "" {
		t, err := time.Parse(time.RFC3339Nano, req.To)
		if err != nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrInvalidTimeFormat,
				"invalid 'to' parameter: expected RFC3339 format")
		}
		filters.To = t
	}

	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}

	result, err := a.S.Query(ctx, filters, pageReq)
	if err != nil {
		return nil, err
	}

	items := make([]*auditlist.ResponseDataItem, 0, len(result.Items))
	for _, e := range result.Items {
		items = append(items, toListResponseDataItem(e))
	}
	return auditlist.List200JSONResponse{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// Handler is the composite route handler for the auditquery slice.
type Handler struct {
	listH *auditlist.Handler
}

// NewHandler creates an auditquery Handler with the generated list handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{
		listH: auditlist.NewHandler(ListAdapter{svc}, auditQueryPolicy),
	}
}

// RegisterRoutes mounts the audit list contract on mux.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	return h.listH.RegisterRoutes(mux)
}

// toListResponseDataItem converts a ledger.Entry to auditlist.ResponseDataItem.
// B2-C-09: Payload is scrubbed of sensitive fields via pkg/redaction.RedactPayload
// before being returned to API consumers.
func toListResponseDataItem(e *ledger.Entry) *auditlist.ResponseDataItem {
	return &auditlist.ResponseDataItem{
		ID:        e.ID,
		EventId:   e.EventID,
		EventType: e.EventType,
		ActorId:   e.ActorID,
		Timestamp: e.Timestamp.Format(time.RFC3339),
		Payload:   json.RawMessage(redaction.RedactPayload(e.Payload)),
	}
}
