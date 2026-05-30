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

// List implements auditlist.Service. The request fields (actorId, subjectId,
// from, to, limit, cursor, eventType) are already decoded and basic-validated by
// handler_gen.
// B2-C-09: Payload is redacted of sensitive fields before returning to client.
//
// subjectId scoping (#1290): subjectId is a plain additional filter and needs NO
// dedicated policy gate. auditQueryPolicy already forces non-admin callers to
// actor_id = self (actorID defaulting below), so a non-admin's query is always
// AND-ed with actor_id = self; a subjectId filter can therefore only narrow
// within the caller's own actions and never reaches another user's rows. Admins
// (actorID may be empty = global) can filter by subjectId to investigate
// impersonation, where the audited action's actor != subject.
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
		EventType: req.EventType,
		ActorID:   actorID,
		SubjectID: req.SubjectID,
	}

	// Inbound from/to filters parse with RFC3339Nano (RFC3339 with optional
	// sub-second precision — the fractional-second segment is allowed, not
	// required): the
	// query window must resolve at the same sub-second granularity the outbound
	// projection emits (see toListResponseDataItem), so a caller can round-trip a
	// returned occurredAt/timestamp verbatim as a filter bound without truncation.
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
//
// Principal/Observability exposure (issue #1229 §4 + #1219): SubjectID,
// CorrelationID and OccurredAt are surfaced (additive, omitempty — empty/zero
// for rows predating PR-A2). CorrelationID is an opaque cross-cell correlation
// id (NOT in pkg/redaction's sensitive-key set), so projecting it is safe and
// lets consumers stitch an audited action back to its originating request.
//
// SessionID is deliberately NOT exposed (it matches pkg/redaction's
// sensitive-key set — a live-session credential-adjacent token; RedactPayload
// only scrubs the nested `payload` object, NOT top-level DTO fields, so adding
// SessionID here would emit a raw token). The audit-domain codegen funnel
// (contractgen, AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01) is the upstream Hard
// backstop: it rejects any sensitive-key field in an audit wire-out schema at
// generation time, so SessionID cannot re-enter ResponseDataItem via schema.
//
// TenantID is not exposed, and — critically — this is NOT a tenant-isolation
// security guarantee (#1289, INV-SINGLE-TENANT-ONLY). develop is single-tenant:
// no producer writes principal.TenantID (the auth middleware never calls
// ctxkeys.WithTenantID — CTXKEYS-PRINCIPAL-WRITE-CALLER-01 locks the only writer
// to consumer-side RestoreToContext), so the column is always empty and there is
// nothing to scope by. The earlier "type-system Hard isolation by absence"
// framing was an over-claim and has been removed: "no tenantId query parameter"
// is not the same as "queries are isolated by tenant". This endpoint performs NO
// tenant scoping whatsoever. Real multi-tenant isolation — a JWT tenant claim
// producer source, AuditFilters.TenantID, and a tenant-scoped WHERE — is tracked
// by epic #1296; the appender carries an INV-SINGLE-TENANT-ONLY tripwire
// (cells/auditcore/internal/appender) that fires loudly if a non-empty tenant
// ever reaches audit persistence before #1296 wires that filtering.
func toListResponseDataItem(e *ledger.Entry) *auditlist.ResponseDataItem {
	// Both audit-evidence timestamps use RFC3339Nano: sub-second precision is
	// part of the evidence (the HMAC chain pins occurred_at/timestamp at nanosecond
	// granularity via *UnixNano), so RFC3339 (second-granularity) would silently
	// truncate the wire projection below the chain's resolution (issue #1229 F6).
	occurredAt := ""
	if !e.OccurredAt.IsZero() {
		occurredAt = e.OccurredAt.Format(time.RFC3339Nano)
	}
	return &auditlist.ResponseDataItem{
		ID:            e.ID,
		EventID:       e.EventID,
		EventType:     e.EventType,
		ActorID:       e.ActorID,
		SubjectID:     e.SubjectID,
		CorrelationID: e.CorrelationID,
		OccurredAt:    occurredAt,
		Timestamp:     e.Timestamp.Format(time.RFC3339Nano),
		Payload:       json.RawMessage(redaction.RedactPayload(e.Payload)),
	}
}
