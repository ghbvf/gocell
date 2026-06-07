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
	"github.com/ghbvf/gocell/pkg/tenant"
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
	return auth.AnyRole(auth.RoleAdmin, auth.RoleSuperAdmin)(r)
}

// auditTenantFilter determines the TenantID filter and validates tenant isolation
// based on the derived row-visibility obligation. Returns the TenantID to set on
// AuditFilters, or an error if the principal cannot issue an audit query.
//
// RowScopeAll (super-admin cross-tenant path, epic #1337 PR-5): skip both the
// F1 empty-tenant rejection and the tenant filter — the store reads across all
// tenants. The mandatory FR-007 audit is already emitted inside p.RowVisibility.
// All other scopes (self/device/tenant): keep the existing F1 isolation guard
// (reject if TenantID is empty) and set the tenant filter from the principal.
func auditTenantFilter(p *auth.Principal, vis tenant.RowVisibility) (string, error) {
	if vis.Scope() == tenant.RowScopeAll {
		return "", nil
	}
	if p.TenantID == "" {
		return "", errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"audit query requires a tenant-scoped principal")
	}
	return p.TenantID, nil
}

// logAdminAuditQuery emits an audit-access breadcrumb when an admin queries the
// ledger (all actors, or a specific other user). Non-admins and admin-self
// queries are silent. Factored out of List for cognitive-complexity budget.
//
// Super-admin access is excluded from this breadcrumb: the mandatory FR-007
// slog.Error cross-tenant audit is already emitted inside p.RowVisibility before
// this function is called. Emitting a second admin-breadcrumb would be redundant
// and confusing (a lower-severity Info record for a higher-privilege event).
func logAdminAuditQuery(ctx context.Context, p *auth.Principal, subject, actorIDFilter string) {
	if p.HasRole(auth.RoleSuperAdmin) {
		return // FR-007 audit already emitted inside p.RowVisibility
	}
	if !p.HasRole(auth.RoleAdmin) {
		return
	}
	switch {
	case actorIDFilter == "":
		slog.InfoContext(ctx, "audit: admin querying all actors", slog.String("admin", subject))
	case actorIDFilter != subject:
		slog.InfoContext(ctx, "audit: admin querying other user",
			slog.String("admin", subject), slog.String("target_actor", actorIDFilter))
	}
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

	// Row-visibility obligation (epic #1337 PR-4/PR-5): derive from principal via
	// the framework derivation. Super-admin → RowScopeAll (cross-tenant; mandatory
	// FR-007 slog.Error audit emitted inside p.RowVisibility). Admin → RowScopeTenant
	// (all actors in their tenant). Non-admin → RowScopeSelf (actor_id == subject).
	// The explicit actorId filter (req.ActorID) is an additional AND predicate on top
	// of the obligation; for non-admins auditQueryPolicy already enforces
	// actorId == "" || == self, so the obligation is the effective enforcement gate.
	vis, err := p.RowVisibility(ctx)
	if err != nil {
		// RowVisibility errors for service/anonymous/unknown principals
		// (KindPermissionDenied). Surface as-is; callers holding a JWT-authenticated
		// user/device principal never reach here under normal circumstances.
		return nil, err
	}

	// Tenant isolation (epic #1337 PR-2a/PR-5): RowScopeAll bypasses F1 guard and
	// tenant filter (super-admin cross-tenant read). All other scopes enforce F1
	// (reject empty TenantID) and set the filter from the principal.
	tenantID, err := auditTenantFilter(p, vis)
	if err != nil {
		return nil, err
	}

	logAdminAuditQuery(ctx, p, subject, req.ActorID)

	filters := ledger.AuditFilters{
		// TenantID: set by auditTenantFilter — non-empty for self/device/tenant scopes
		// (isolation boundary), empty for RowScopeAll (super-admin cross-tenant read,
		// epic #1337 PR-5). Note: audit_entries has NO DB-layer RLS (deferred #1618 —
		// hash chain is namespace-global; tenant isolation here is app-layer only).
		TenantID:  tenantID,
		EventType: req.EventType,
		// ActorID: admin's explicit actor filter (or empty = all). Non-admin
		// callers: auditQueryPolicy already enforces req.ActorID == "" || ==
		// self, but the row-visibility obligation (vis) above is the
		// real enforcement gate — it restricts store results to actor_id == self
		// regardless of this filter. The explicit filter narrows further if set.
		ActorID:   req.ActorID,
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

	result, err := a.S.Query(ctx, vis, filters, pageReq)
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
// TenantID is deliberately NOT exposed per-row. Two read regimes (epic #1337):
//   - self/device/tenant scopes (the default for every non-super-admin caller):
//     the read path IS tenant-scoped — auditTenantFilter sets
//     AuditFilters.TenantID from the principal and the store filters by it, so
//     every returned row already belongs to the caller's own tenant. A per-row
//     tenantId field would be redundant (a constant equal to the caller's own
//     tenant). This replaced the PR-1 (#1339 F2) blanket 403 gate and retired the
//     appender's INV-SINGLE-TENANT-ONLY tripwire (#1289); a principal with an
//     empty tenant is rejected at the List boundary (F1).
//   - super-admin RowScopeAll (PR-5): the read IS cross-tenant — auditTenantFilter
//     drops the tenant filter, so returned rows may span tenants and a per-row
//     tenantId would NOT be a redundant constant. Surfacing per-row tenant
//     attribution for the cross-tenant view is a known, deliberate limitation of
//     PR-5 (#1759 F3): it is deferred to the PR-11/12 column-level
//     ResourceProjection work (spec T12.2 / #1219), which lands the wire field
//     together with FieldMask masking rather than ad-hoc here. Until then a
//     super-admin correlates rows to tenants via the audited payload, not a
//     dedicated DTO field. audit_entries has NO DB-layer RLS (deferred #1618);
//     cross-tenant isolation for non-super-admins is app-layer (the obligation +
//     tenant filter above), not DB RLS.
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
