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
//     treats admin callers as tenant-wide queries.
//   - OR actorId equals authenticated subject (self-access)
//   - OR subject has the "admin" role
//
// Tenant isolation (epic #1337 PR-2a): every query is tenant-scoped. The List
// adapter always sets AuditFilters.TenantID from the authenticated principal and
// the store filters by it, so a caller can only ever read its own tenant's audit
// trail (admin-ness widens the actor axis, never the tenant axis). This handler
// is the isolation boundary — it replaced the PR-1 (#1339 F2) blanket 403
// fail-closed gate that rejected every tenant-bearing caller outright because no
// tenant-scoped read path existed yet.
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
	// Tenant isolation fail-closed (epic #1337 PR-2a, F1): a tenant-scoped audit
	// read REQUIRES a concrete tenant. An authenticated principal with an empty
	// TenantID cannot establish an isolation scope; rather than fall through to
	// the store's "empty TenantID = no filter = all tenants" semantics (a
	// cross-tenant read), reject here. Post-PR-2a every access token carries
	// tenant_id (login requires it; sessionmint stamps it), so this only triggers
	// for malformed/legacy tokens — never the normal path. This is the isolation
	// boundary; canonical-UUID form is already enforced by the JWT authenticator.
	if p.TenantID == "" {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"audit query requires a tenant-scoped principal")
	}
	subject := p.Subject

	actorID := req.ActorID
	if actorID == "" && !p.HasRole(auth.RoleAdmin) {
		actorID = subject
	}
	switch {
	case actorID == "":
		slog.InfoContext(ctx, "audit: admin querying all actors", slog.String("admin", subject))
	case actorID != subject:
		slog.InfoContext(ctx,
			"audit: admin querying other user",
			slog.String("admin", subject),
			slog.String("target_actor", actorID),
		)
	}

	filters := ledger.AuditFilters{
		// TenantID is the isolation scope (epic #1337 PR-2a): sourced from the
		// authenticated principal, never from a request field, so a caller reads
		// its OWN tenant's audit trail PLUS tenant-less system events (e.g.
		// bootstrap.auth.fail) — never another tenant's rows (see
		// ledger.AuditFilters.TenantID). p.TenantID is guaranteed non-empty here
		// (the empty case is rejected above — F1); DB-layer RLS (PR-3) is
		// defense-in-depth for the non-empty path. This always-set-from-principal
		// step is the isolation boundary.
		TenantID:  p.TenantID,
		EventType: req.EventType,
		ActorID:   actorID,
		SubjectID: req.SubjectID,
		TraceID:   req.TraceID,
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
// TenantID is deliberately NOT exposed per-row. This is no longer a fail-open
// gap: as of epic #1337 PR-2a the read path IS tenant-scoped — the List adapter
// always sets AuditFilters.TenantID from the authenticated principal, so every
// returned row already belongs to the caller's own tenant. A per-row tenantId
// field would therefore be redundant (a constant equal to the caller's own
// tenant), so it is omitted. This replaced the PR-1 (#1339 F2) blanket 403 gate
// and retired the appender's INV-SINGLE-TENANT-ONLY tripwire (#1289). A principal
// with an empty tenant is rejected at the List boundary (F1), so the read path is
// never tenant-unscoped; DB-layer RLS (PR-3) is defense-in-depth.
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
		TraceID:       e.TraceID,
		OccurredAt:    occurredAt,
		Timestamp:     e.Timestamp.Format(time.RFC3339Nano),
		Payload:       json.RawMessage(redaction.RedactPayload(e.Payload)),
	}
}
