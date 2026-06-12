package auditquery

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	auditlist "github.com/ghbvf/gocell/generated/contracts/http/audit/list/v1"
	cell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/projection"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
)

// auditQueryPolicy permits the request when:
//   - actorId query param EQUALS the authenticated subject (explicit self-read).
//     This is a request-shape check: it answers "is the caller asking only for its
//     OWN actor rows?" — a parameter==subject ownership match, NOT a role-literal
//     authorization branch. A pure self-read needs no audit:read permission.
//   - OTHERWISE (actorId empty, or set and != subject) the PDP (wired ABAC
//     Authorizer in context) must grant authz.PermAuditRead().
//
// EMPTY actorId is deliberately NOT exempt (F1, codex review): for an admin /
// super-admin it is a ledger-WIDE read across every actor, which is exactly the
// cross-actor read audit:read gates. Exempting it would let an admin's global
// read skip the PDP entirely (bypassing a tenant forbid policy or an unwired PDP).
// A non-admin who wants only its own audit rows names itself explicitly
// (actorId=<subject>); an empty actorId is treated as a permissioned ledger read,
// not an implicit self-read. The gate stays role-literal-free: it never inspects
// roles — whether an admin/super-admin's empty-actorId read is granted is decided
// by the PDP baseline (admin/super-admin → audit:read), and a non-admin without
// audit:read is denied. This replaced the role-literal
// auth.AnyRole(RoleAdmin, RoleSuperAdmin) gate removed in #914
// (PERMISSION-BASED-AUTHZ-01).
//
// Row visibility is governed independently at the data layer by the principal's
// RowScope (self → own rows only), NOT by this gate; the gate is a coarse
// allow/deny on the audit:read permission.
//
// Tenant isolation (epic #1337 PR-2a, typed param #1618): every query is
// tenant-scoped. The List adapter always passes the typed tenant.TenantID parsed
// from the authenticated principal to Service.Query → Store.Query, which scopes
// results to that tenant (plus tenant-less system rows), so a caller can only ever
// read its own tenant's audit trail (admin-ness widens the actor axis, never the
// tenant axis). This handler is the isolation boundary — it replaced the PR-1
// (#1339 F2) blanket 403 fail-closed gate that rejected every tenant-bearing caller
// outright because no tenant-scoped read path existed yet.
//
// SelfOr cannot be used here because "self" is determined by the actorId query
// parameter, not a path parameter.
func auditQueryPolicy(r *http.Request) error {
	ctx := r.Context()
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	// Only an explicit self-read (actorId names the caller) is exempt. Empty
	// actorId is a permissioned ledger read, not an implicit self-read (F1).
	if r.URL.Query().Get("actorId") == p.Subject {
		return nil
	}
	return auth.RequirePermission(authz.PermAuditRead())(r)
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
// dedicated policy gate. The row-visibility obligation (vis, below) restricts a
// non-admin caller to RowScope=self (actor_id == subject) regardless of filters,
// so a non-admin's query is always AND-ed with actor_id = self; a subjectId filter
// can therefore only narrow within the caller's own actions and never reaches
// another user's rows. Admins (RowScope=tenant, actorID may be empty = global)
// can filter by subjectId to investigate impersonation, where the audited action's
// actor != subject. Reaching this point at all required passing auditQueryPolicy
// (explicit self-read, or audit:read granted by the PDP).
func (a ListAdapter) List(ctx context.Context, req *auditlist.Request) (auditlist.ListResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	// Tenant isolation fail-closed (epic #1337 PR-2a, F1): a tenant-scoped audit
	// read REQUIRES a concrete tenant. An authenticated principal with an empty
	// TenantID cannot establish an isolation scope; rather than let an empty
	// tenant degrade the read to the store's system-chain (tenant_id = '' rows
	// only — never the caller's intended tenant data), reject here. Service.Query
	// is the defense-in-depth backstop (it also rejects an empty post-auth tenant,
	// #1618 F2). Post-PR-2a every access token carries tenant_id (login requires
	// it; sessionmint stamps it), so this only triggers for malformed/legacy
	// tokens — never the normal path. This is the isolation boundary;
	// canonical-UUID form is already enforced by the JWT authenticator.
	if p.TenantID == "" {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"audit query requires a tenant-scoped principal")
	}
	subject := p.Subject

	// Row-visibility obligation (epic #1337 PR-4/PR-5): derive from principal via
	// the framework derivation. Super-admin → RowScopeAll, Admin → RowScopeTenant
	// (all actors in their tenant), Non-admin → RowScopeSelf (actor_id == subject).
	// NOTE (#1618 merge): under per-tenant FORCE RLS the audit store fail-closes
	// RowScopeAll (RowScopeAllUnsupportedError) — cross-tenant audit read by a
	// super-admin is deferred to backlog (the NOBYPASSRLS serving role cannot
	// enumerate tenants); the mandatory FR-007 slog.Error audit is still emitted
	// inside p.RowVisibility regardless. The explicit actorId filter (req.ActorID)
	// is an additional AND predicate on top of the obligation; for non-admins
	// auditQueryPolicy already enforces actorId == "" || == self, so the obligation
	// is the effective enforcement gate.
	vis, err := p.RowVisibility(ctx)
	if err != nil {
		// RowVisibility errors for service/anonymous/unknown principals
		// (KindPermissionDenied). Surface as-is; callers holding a JWT-authenticated
		// user/device principal never reach here under normal circumstances.
		return nil, err
	}

	logAdminAuditQuery(ctx, p, subject, req.ActorID)

	// Column masking (epic #1337 PR-12, FR-016/FR-017): derive the mask obligation
	// from the row-visibility scope ONCE here — it gates both the query predicates
	// (rejectMaskedFilters, below) and the response projection (NewProjectionList,
	// below). A masked column must not be usable as a filter, else the predicate
	// leaks a match/no-match oracle on a value the response redacts (F1).
	mask := auditFieldMask(vis.Scope())
	if err := rejectMaskedFilters(mask, req); err != nil {
		return nil, err
	}

	// Tenant axis (#1618): the typed tenant scope, re-parsed from the
	// authenticated principal at this repo boundary (auth.Principal.TenantID is a
	// canonicalized string; the JWT authenticator already validated it). It is
	// passed to Store.Query as the mandatory t param so a caller reads its OWN
	// tenant's audit trail PLUS tenant-less system events (bootstrap.auth.fail) —
	// never another tenant's rows. p.TenantID is guaranteed non-empty here (the
	// empty case is rejected above — F1). This always-from-principal step is the
	// app-layer isolation boundary; the Service wraps the read in a tenant-scoped
	// RunInTx so DB-layer FORCE RLS is the defense-in-depth backstop.
	tid, err := tenant.ParseTenantID(p.TenantID)
	if err != nil {
		// Unreachable on the normal path (the JWT authenticator canonicalizes the
		// claim); a malformed principal tenant is a server-side invariant break.
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"audit query: principal tenant is not canonical", err)
	}

	filters := ledger.AuditFilters{
		EventType: req.EventType,
		// ActorID: admin's explicit actor filter (or empty = all). Non-admin
		// callers: the row-visibility obligation (vis) above is the real
		// enforcement gate — it restricts store results to actor_id == self
		// regardless of this filter. The explicit filter narrows further if set.
		// (auditQueryPolicy already gated entry: a non-admin reached here only via
		// an explicit self-read or by holding audit:read.)
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

	result, err := a.S.Query(ctx, tid, vis, filters, pageReq)
	if err != nil {
		return nil, err
	}

	// Discharge the column mask derived above onto every row (the same obligation
	// that gated the query predicates), via the sealed projection funnel.
	rows := make([]map[string]any, 0, len(result.Items))
	for _, e := range result.Items {
		rows = append(rows, toListResponseDataItem(e).ToMap())
	}
	data, err := projection.NewProjectionList(mask, rows)
	if err != nil {
		// A mask this PEP cannot discharge (e.g. a nested-path obligation) is a
		// server-side misconfiguration, not a client error — fail closed (the
		// errcode kind maps to 500) rather than serve an un-masked column.
		return nil, err
	}
	return auditlist.List200JSONResponse{
		Data:       data,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// auditFieldMask derives the column-mask obligation for an audit read from the
// caller's row-visibility scope (epic #1337 PR-12, FR-017). It is the PR-12-era
// source of the FieldMask obligation: an admin/super-admin (RowScopeTenant /
// RowScopeAll) sees every column (empty mask = identity projection), while a
// non-admin user (RowScopeSelf) and a device (RowScopeDevice) have the
// operator-diagnostic / cross-subject columns masked. A device additionally
// cannot see the human subject-of-record. This deterministic identity→mask
// derivation is replaced by the ABAC Decision.Obligations().FieldMask once the
// policy engine is wired into the request path (PR-10 #1348); the projection PEP
// that discharges the mask is unchanged by that swap.
//
// Fail-closed default (defense-in-depth): any future or unknown RowScope value
// that is not explicitly enumerated here falls through to the MOST RESTRICTIVE
// mask (same as RowScopeDevice). RowVisibility already validates scopes upstream,
// so this branch is never reached on the normal path — it is a compile-time safety
// net that prevents an unenumerated scope from silently widening column visibility.
//
// sessionId is never on the wire (it is not a projected column — the
// AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 codegen guard keeps it out of the response
// schema), and payload is already scrubbed by RedactPayload, so neither needs a
// per-scope mask entry here.
func auditFieldMask(scope tenant.RowScope) authz.FieldMask {
	switch scope {
	case tenant.RowScopeSelf:
		return authz.FieldMask{Fields: append([]string(nil), auditMaskDiagnostics...)}
	case tenant.RowScopeDevice:
		return authz.FieldMask{Fields: append([]string(nil), auditMaskDevice...)}
	case tenant.RowScopeTenant, tenant.RowScopeAll:
		// admin / super-admin: full column view (identity projection).
		return authz.IdentityFieldMask()
	default:
		// Unknown/unenumerated scope: fail-closed with the most restrictive mask
		// (same as a device). An unenumerated scope must never silently widen
		// column visibility.
		return authz.FieldMask{Fields: append([]string(nil), auditMaskDevice...)}
	}
}

// Audit column names that the per-scope FieldMask obligation governs. These are
// the wire JSON keys (camelCase) the ResourceProjection funnel masks; they are
// also the columns a caller may NOT use as a query predicate when masked (a
// masked-column filter would leak a match/no-match oracle — see
// rejectMaskedFilters). Defined once as consts so the mask derivation and the
// filter gate cannot drift to differently-spelled literals.
const (
	auditColSubjectID     = "subjectId"
	auditColCorrelationID = "correlationId"
	auditColTraceID       = "traceId"
)

// Pre-built per-scope mask column sets. auditMaskDiagnostics masks the
// operator-diagnostic columns (non-admin self); auditMaskDevice additionally
// masks the human subject-of-record (device, and the fail-closed default). The
// FieldMask constructor copies these so the package-level slices stay immutable.
var (
	auditMaskDiagnostics = []string{auditColCorrelationID, auditColTraceID}
	auditMaskDevice      = []string{auditColSubjectID, auditColCorrelationID, auditColTraceID}
)

// rejectMaskedFilters fails closed when a caller supplies a query predicate on a
// column its row-visibility scope masks (epic #1337 PR-12, F1). Column masking is
// an obligation the PEP must discharge on the WHOLE read, not just response
// serialization: leaving a masked column usable as a filter would leak a
// match/no-match oracle — a non-admin could binary-search the very traceId the
// response redacts, a device could enumerate the subject it cannot see. Only
// columns that are BOTH filterable and maskable need gating; for the audit read
// that is subjectId and traceId (correlationId is masked but has no filter param).
// Returns KindPermissionDenied (403, already a declared response): the caller is
// not malformed, it is asking to filter by a column outside its visibility.
func rejectMaskedFilters(mask authz.FieldMask, req *auditlist.Request) error {
	maskedFilters := []struct {
		column string
		value  string
	}{
		{auditColSubjectID, req.SubjectID},
		{auditColTraceID, req.TraceID},
	}
	for _, mf := range maskedFilters {
		if mf.value != "" && mask.Masks(mf.column) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
				"audit query cannot filter by a column masked for your access scope",
				errcode.WithDetails(errcode.PublicString("column", mf.column)))
		}
	}
	return nil
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
// TenantID IS now projected per-row (epic #1337 PR-12, FR-016) — reversing the
// earlier "deliberately omitted as redundant" stance. It is surfaced behind the
// ResourceProjection column-masking funnel (the List adapter feeds this DTO's
// ToMap() to projection.NewProjectionList), so a per-row tenantId is no longer an
// unconditional leak: it is a maskable column the FieldMask obligation governs.
// tenantId is NOT in pkg/redaction's sensitive-key set (it is an opaque tenant
// UUID, not a credential), so the AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 codegen
// guard admits it; the per-scope auditFieldMask currently leaves it visible (an
// own-tenant row's id is the caller's own tenant), but the column is now
// addressable so a future cross-tenant super-admin read (or an ABAC policy) can
// mask or surface it without a wire-shape change. Empty for tenant-less system
// rows (scope="system"). The mapping into the DTO is unconditional; visibility is
// the funnel's job, not the converter's.
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
		TenantID:      e.TenantID,
		CorrelationID: e.CorrelationID,
		TraceID:       e.TraceID,
		OccurredAt:    occurredAt,
		Timestamp:     e.Timestamp.Format(time.RFC3339Nano),
		Scope:         rowScope(e.TenantID),
		Payload:       json.RawMessage(redaction.RedactPayload(e.Payload)),
	}
}

// rowScope classifies an audit row relative to the calling tenant for the wire
// `scope` field (#1618 review F7). A tenant-scoped query returns the caller's own
// rows PLUS tenant-less system/framework rows (tenant_id == "" — e.g.
// bootstrap.auth.fail), which the per-tenant RLS `OR tenant_id=”` read clause
// surfaces to every tenant. Marking each row "system" vs "tenant" lets a tenant
// admin distinguish global system events from their own audit trail. (Per-row
// tenantId IS now projected since PR-12 — see toListResponseDataItem — but it is
// always the caller's OWN tenant id, so `scope` remains the system-vs-tenant
// signal; it never carries another tenant's id.)
func rowScope(tenantID string) string {
	if tenantID == "" {
		return scopeSystem
	}
	return scopeTenant
}

const (
	scopeSystem = "system"
	scopeTenant = "tenant"
)
