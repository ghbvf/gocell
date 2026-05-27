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

// auditQueryPolicy permits the request when:
//   - actorId query param is empty. List scopes non-admin callers to self and
//     treats admin callers as global queries.
//   - OR actorId equals authenticated subject (self-access)
//   - OR subject has the "admin" role
//
// SelfOr cannot be used here because "self" is determined by the actorId query
// parameter, not a path parameter.
// role-name literal will be migrated to permission-based authz when that work lands.
// Deferred (S43, tracked by gh issue #914 — PERMISSION-BASED-AUTHZ-01).
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
// It handles actor scoping, time parsing, and pagination mapping.
type ListAdapter struct {
	S *Service
}

// List implements auditlist.Service. The request fields (actorId, subjectId, tenantId,
// sessionId, correlationId, from, to, limit, cursor, eventType) are already decoded
// and basic-validated by handler_gen.
// B2-C-09: Payload is redacted of sensitive fields before returning to client.
func (a ListAdapter) List(ctx context.Context, req *auditlist.Request) (auditlist.ListResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	subject := p.Subject

	actorID := req.ActorID
	if actorID == "" && !p.HasRole(auth.RoleAdmin) {
		actorID = subject
	}
	switch {
	case actorID == "":
		slog.Info("audit: admin querying all actors", slog.String("admin", subject))
	case actorID != subject:
		slog.Info(
			"audit: admin querying other user",
			slog.String("admin", subject),
			slog.String("target_actor", actorID),
		)
	}

	filters := ledger.AuditFilters{
		EventType:     req.EventType,
		ActorID:       actorID,
		SubjectID:     req.SubjectID,
		TenantID:      req.TenantID,
		SessionID:     req.SessionID,
		CorrelationID: req.CorrelationID,
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
// F11: SubjectID/TenantID/SessionID/CorrelationID/OccurredAt (Principal envelope fields)
// are exposed as optional top-level fields; OccurredAt is omitted when zero.
func toListResponseDataItem(e *ledger.Entry) *auditlist.ResponseDataItem {
	item := &auditlist.ResponseDataItem{
		ID:            e.ID,
		EventID:       e.EventID,
		EventType:     e.EventType,
		ActorID:       e.ActorID,
		SubjectID:     e.SubjectID,
		TenantID:      e.TenantID,
		SessionID:     e.SessionID,
		CorrelationID: e.CorrelationID,
		Timestamp:     e.Timestamp.Format(time.RFC3339),
		Payload:       json.RawMessage(redaction.RedactPayload(e.Payload)),
	}
	if !e.OccurredAt.IsZero() {
		item.OccurredAt = e.OccurredAt.UTC().Format(time.RFC3339)
	}
	return item
}
