package auditquery

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	cell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	"github.com/ghbvf/gocell/framework/pkg/projection"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	auditget "github.com/ghbvf/gocell/generated/contracts/http/audit/get/v1"
	auditlist "github.com/ghbvf/gocell/generated/contracts/http/audit/list/v1"
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
// The logger parameter is the Service's injected *slog.Logger (not the global
// slog default). Using the injected logger ensures that tests capturing the
// injected logger can observe — and therefore guard — the CWE-117 ordering
// invariant (validate before log). Using the global slog default would make
// the TestHandleQuery_FilterValidation_ValidationBeforeLogging test vacuous:
// breadcrumbs would go to global slog while the test watches the injected handler.
//
// Super-admin access is excluded from this breadcrumb: the mandatory FR-007
// slog.Error cross-tenant audit is emitted inside p.CrossTenantVisibility on the
// super-admin path. Emitting a second admin-breadcrumb would be redundant and
// confusing (a lower-severity Info record for a higher-privilege event).
func logAdminAuditQuery(ctx context.Context, logger *slog.Logger, p *auth.Principal, subject, actorIDFilter string) {
	if p.HasRole(auth.RoleSuperAdmin) {
		return // FR-007 audit already emitted inside p.CrossTenantVisibility
	}
	if !p.HasRole(auth.RoleAdmin) {
		return
	}
	switch {
	case actorIDFilter == "":
		logger.InfoContext(ctx, "audit: admin querying all actors", slog.String("admin", subject))
	case actorIDFilter != subject:
		logger.InfoContext(ctx, "audit: admin querying other user",
			slog.String("admin", subject), slog.String("target_actor", actorIDFilter))
	}
}

// auditVisibilityResult carries the outcome of deriveAuditVisibility.
type auditVisibilityResult struct {
	vis           tenant.RowVisibility
	ctv           tenant.CrossTenantVisibility // non-zero only when isCrossTenant
	isCrossTenant bool
}

// deriveAuditVisibility derives the row-visibility obligation for a single
// audit List request, routing super-admin through CrossTenantVisibility (the
// sole FR-007-audited funnel) and all other principals through RowVisibility.
// Exactly one mint happens per call — calling both for the same request would
// double the FR-007 audit record.
//
// Routing: super-admin calls p.CrossTenantVisibility(ctx) [emits FR-007 slog.Error];
// all others call p.RowVisibility(ctx) [no FR-007 audit].
func deriveAuditVisibility(ctx context.Context, p *auth.Principal) (auditVisibilityResult, error) {
	if p.HasRole(auth.RoleSuperAdmin) {
		ctv, err := p.CrossTenantVisibility(ctx)
		if err != nil {
			return auditVisibilityResult{}, err
		}
		return auditVisibilityResult{
			vis:           ctv.Visibility(),
			ctv:           ctv,
			isCrossTenant: true,
		}, nil
	}
	vis, err := p.RowVisibility(ctx)
	if err != nil {
		return auditVisibilityResult{}, err
	}
	return auditVisibilityResult{vis: vis}, nil
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

	// Row-visibility obligation (epic #1337 PR-4/PR-5, #1810): derive EXACTLY ONE
	// visibility mint per request via deriveAuditVisibility.
	//
	// super-admin path: calls p.CrossTenantVisibility(ctx) which emits the
	//   mandatory FR-007 slog.Error audit, then routes to Service.QueryCrossTenant
	//   (admin-pool-backed, no per-tenant GUC). No p.RowVisibility call on this path.
	// non-super-admin path: calls p.RowVisibility(ctx) (no FR-007 audit), routes to
	//   Service.Query with the per-tenant RunInTx wrapper.
	//
	// Calling both for a single request would double the FR-007 audit record;
	// deriveAuditVisibility enforces single-mint.
	vr, err := deriveAuditVisibility(ctx, p)
	if err != nil {
		// RowVisibility / CrossTenantVisibility errors for service/anonymous/unknown
		// principals (KindPermissionDenied). Surface as-is.
		return nil, err
	}
	vis := vr.vis

	// CWE-117 ordering: validate filter inputs BEFORE any logging of request
	// fields (buildAuditFilters is the wire-boundary gate; logAdminAuditQuery
	// must not receive unvalidated input — a malformed actorId would otherwise
	// appear in log records before the 400 is returned).
	filters, err := buildAuditFilters(req)
	if err != nil {
		return nil, err
	}

	logAdminAuditQuery(ctx, a.S.logger, p, subject, req.ActorID)

	// Column masking (epic #1337 PR-12, FR-016/FR-017): derive the mask obligation
	// from the row-visibility scope ONCE here — it gates both the query predicates
	// (rejectMaskedFilters, below) and the response projection (NewProjectionList,
	// below). A masked column must not be usable as a filter, else the predicate
	// leaks a match/no-match oracle on a value the response redacts (F1).
	// For RowScopeAll (super-admin cross-tenant) auditFieldMask returns identity
	// mask (full column view) — same as RowScopeTenant.
	mask := auditFieldMask(vis.Scope())
	if err := rejectMaskedFilters(mask, req); err != nil {
		return nil, err
	}

	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}

	result, err := a.executeQuery(ctx, p, vr, vis, filters, pageReq)
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

// buildAuditFilters validates and parses the request filter fields into an
// AuditFilters. Extracted from List to keep cognitive complexity ≤ 15.
//
// This is the wire-boundary validation gate (CWE-117 / #1742): it must run
// BEFORE any logging of req fields. The handler's List function calls this
// before logAdminAuditQuery so an invalid actorId is never logged.
//
// ID fields (actorId, subjectId, traceId): validated via idutil.SafeID.Validate
// (SafeID charset + MaxMetadataIDLen length cap). Empty is allowed.
//
// eventType: length cap only (len > MaxMetadataIDLen). EventType is a dotted
// label and uses characters outside the SafeID charset (e.g. dots), so only
// a length cap is enforced here.
//
// Inbound from/to use RFC3339Nano (optional sub-second precision) so a caller
// can round-trip a returned occurredAt/timestamp verbatim as a filter bound
// without truncation.
func buildAuditFilters(req *auditlist.Request) (ledger.AuditFilters, error) {
	if err := validateIDFilters(req); err != nil {
		return ledger.AuditFilters{}, err
	}
	filters := ledger.AuditFilters{
		EventType: req.EventType,
		ActorID:   req.ActorID,
		SubjectID: req.SubjectID,
		TraceID:   req.TraceID,
	}
	if req.From != "" {
		t, err := time.Parse(time.RFC3339Nano, req.From)
		if err != nil {
			return ledger.AuditFilters{}, errcode.New(errcode.KindInvalid, errcode.ErrInvalidTimeFormat,
				"invalid 'from' parameter: expected RFC3339 format")
		}
		filters.From = t
	}
	if req.To != "" {
		t, err := time.Parse(time.RFC3339Nano, req.To)
		if err != nil {
			return ledger.AuditFilters{}, errcode.New(errcode.KindInvalid, errcode.ErrInvalidTimeFormat,
				"invalid 'to' parameter: expected RFC3339 format")
		}
		filters.To = t
	}
	return filters, nil
}

// validateIDFilters validates the ID-typed query filter parameters from the
// wire request before any logging or store access. Extracted from buildAuditFilters
// to keep cognitive complexity ≤ 15.
//
// Empty values are allowed ("no filter"). Violations yield KindInvalid /
// ErrValidationFailed → HTTP 400 (already declared in contract.yaml).
//
// INTENTIONAL TWO-LAYER DESIGN: this function is the wire-boundary gate (layer 1).
// It runs at the handler before any logging (CWE-117: never log unvalidated input)
// and returns a client-facing 400. ledger.ValidateQueryFilters is the store-layer
// defense-in-depth chokepoint (layer 2) shared by all backends including
// CrossTenantQueryStore, guarding non-handler callers such as internal tooling,
// direct store access, and cross-tenant read paths. Both layers call the same
// idutil.SafeID validation and MaxMetadataIDLen cap — they must NOT be merged
// (the handler layer must stay at the wire boundary; the ledger layer must stay
// at the store entry). The apparent duplication is load-bearing security layering.
func validateIDFilters(req *auditlist.Request) error {
	if err := idutil.SafeID(req.ActorID).Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid query parameter: actorId format",
			errcode.WithInternal(errcode.InternalAttr("field", "actorId")))
	}
	if err := idutil.SafeID(req.SubjectID).Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid query parameter: subjectId format",
			errcode.WithInternal(errcode.InternalAttr("field", "subjectId")))
	}
	if err := idutil.SafeID(req.TraceID).Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid query parameter: traceId format",
			errcode.WithInternal(errcode.InternalAttr("field", "traceId")))
	}
	if len(req.EventType) > idutil.MaxMetadataIDLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid query parameter: eventType too long",
			errcode.WithInternal(errcode.InternalAttr("field", "eventType")))
	}
	return nil
}

// requireAuditReadForCrossTenant enforces the audit:read PDP check unconditionally
// on the cross-tenant (super-admin) path. The route-level auditQueryPolicy exempts
// an explicit self-read (actorId == subject) from the PDP — that exemption is sound
// for tenant-scoped reads but UNSOUND for cross-tenant reads: a super-admin with
// ?actorId=<self> would otherwise bypass any tenant deny policy for a cross-tenant
// operation (F1, Codex review). This helper is the defense: regardless of the
// actorId parameter, every cross-tenant read MUST pass audit:read at the PDP.
//
// Mechanism: uses AuthorizerFromContext (the sole sealed reader of the authorizer
// funnel) and evaluatePermissionDecision (the shared Decision→error mapper), both
// from runtime/auth/permission.go, mirroring RequirePermission's decision logic but
// callable from a handler with a plain ctx rather than *http.Request.
const msgCrossTenantPermDenied = "cross-tenant audit read requires audit:read permission"

func requireAuditReadForCrossTenant(ctx context.Context, subject string) error {
	p := authz.PermAuditRead()
	authorizer, ok := auth.AuthorizerFromContext(ctx)
	if !ok {
		// Fail-closed: an unwired PDP must never permit a cross-tenant read.
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossTenantPermDenied)
	}
	dec, err := authorizer.Authorize(ctx, subject, "", p.String())
	if err != nil {
		return err
	}
	if !dec.IsAllow() {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossTenantPermDenied)
	}
	// Obligations on the cross-tenant path are not dischargeable at this gate;
	// fail-closed if the PDP attaches any (mirrors RequirePermission's F5 rule).
	if obl := dec.Obligations(); !obl.IsZero() {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossTenantPermDenied)
	}
	return nil
}

// executeQuery dispatches the paged audit query to the correct service method:
// cross-tenant (super-admin + admin pool) or tenant-scoped (all others).
// Extracted from List to keep cognitive complexity ≤ 15.
func (a ListAdapter) executeQuery(
	ctx context.Context, p *auth.Principal, vr auditVisibilityResult,
	vis tenant.RowVisibility, filters ledger.AuditFilters, pageReq query.PageParams,
) (query.PageResult[*ledger.Entry], error) {
	if vr.isCrossTenant {
		// F1: the cross-tenant path ALWAYS requires audit:read from the PDP,
		// regardless of actorId==subject (the route-level self-read exemption is
		// unsound here — a super-admin self-read is still cross-tenant). This check
		// is independent of and additive to the route-level auditQueryPolicy gate.
		if err := requireAuditReadForCrossTenant(ctx, p.Subject); err != nil {
			return query.PageResult[*ledger.Entry]{}, err
		}
		return a.S.QueryCrossTenant(ctx, vr.ctv, filters, pageReq)
	}
	// Tenant axis (#1618): typed tenant scope re-parsed from the authenticated
	// principal. Passed to Store.Query so a caller reads its OWN tenant's audit
	// trail plus tenant-less system events — never another tenant's rows.
	// p.TenantID is guaranteed non-empty (the empty case is rejected in List).
	tid, err := tenant.ParseTenantID(p.TenantID)
	if err != nil {
		// Unreachable on the normal path (the JWT authenticator canonicalizes the
		// claim); a malformed principal tenant is a server-side invariant break.
		return query.PageResult[*ledger.Entry]{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"audit query: principal tenant is not canonical", err)
	}
	return a.S.Query(ctx, tid, vis, filters, pageReq)
}

// GetAdapter wraps Service to implement auditget.Service for http.audit.get.v1.
// It is the single-entry read counterpart of ListAdapter: it fetches one audit
// entry by its opaque path-param id, reusing the SAME tenant/RLS + row-visibility
// + column-masking + payload-redaction machinery as the list read.
//
// Authorization model (flat audit:read, #1852): the route gate is
// auth.RequirePermission(authz.PermAuditRead()) — there is NO actorId-self
// exemption like the list's auditQueryPolicy, because the path param is the ENTRY
// id, not an actor identity, so "is the caller asking only for its own actor rows?"
// cannot be answered at the gate. A non-admin's own-entry detail is served by the
// list (?actorId=<self>, which returns the matching rows with the SAME field set as
// this endpoint — the field sets are maintained separately per contract, see
// toGetResponseDataItem vs toListResponseDataItem, so equality is a current design
// choice, not a structural guarantee). Self-scoping is still enforced independently
// at the data layer by the principal's RowScope (a RowScopeSelf caller only resolves
// entries whose actor_id is itself; others collapse to 404 IDOR-safe), so the flat
// gate does not widen data access (D3).
//
// Audit-access breadcrumb: unlike ListAdapter (which emits logAdminAuditQuery when an
// admin enumerates the ledger), the detail read emits NO per-request admin breadcrumb
// by design. A by-id read is a TARGETED fetch, not enumeration — the caller must
// already hold the specific entry id, which it obtained from a list query that WAS
// breadcrumbed (admin) or row-scoped (self). The super-admin cross-tenant path still
// emits the mandatory FR-007 slog.Error inside deriveAuditVisibility. Adding a
// per-detail admin breadcrumb here would require a HasRole branch (PERMISSION-BASED-
// AUTHZ-01 allowlist churn) for marginal coverage over the already-audited list.
type GetAdapter struct {
	S *Service
}

// Get implements auditget.Service. The path-param id is already length-bounded
// (1..256) by handler_gen; this adapter additionally validates it as an
// idutil.SafeID (charset) — the audit entry id is an opaque backend-agnostic handle
// treated exactly like the list's actorId/subjectId/traceId filters, NOT a
// format:uuid, so a malformed id is a 400 here (the store separately parse-guards
// the PG uuid lookup). Exactly ONE visibility mint per request (mirrors ListAdapter
// via deriveAuditVisibility): super-admin routes to GetByIDCrossTenant, all others
// to the tenant-scoped GetByID.
func (a GetAdapter) Get(ctx context.Context, req *auditget.Request) (auditget.GetResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	// Tenant isolation fail-closed (epic #1337 PR-2a, F1), mirrors ListAdapter: a
	// tenant-scoped audit read REQUIRES a concrete tenant.
	if p.TenantID == "" {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"audit query requires a tenant-scoped principal")
	}
	// Wire-boundary validation (CWE-117): the opaque id is validated as a SafeID
	// before any store access or logging. Empty/over-length is already rejected by
	// handler_gen; this adds the charset check (consistent with validateIDFilters).
	if err := idutil.SafeID(req.ID).Validate(); err != nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid path parameter: id format",
			errcode.WithInternal(errcode.InternalAttr("field", "id")))
	}

	vr, err := deriveAuditVisibility(ctx, p)
	if err != nil {
		return nil, err
	}
	vis := vr.vis

	entry, err := a.getEntry(ctx, p, vr, vis, req.ID)
	if err != nil {
		return nil, err
	}

	// Discharge the column mask derived from the row-visibility scope onto the
	// single row (same obligation/funnel as the list — NewProjection is the single
	// counterpart). For RowScopeAll (super-admin) auditFieldMask is the identity
	// mask (full view), same as RowScopeTenant.
	mask := auditFieldMask(vis.Scope())
	data, err := projection.NewProjection(mask, toGetResponseDataItem(entry).ToMap())
	if err != nil {
		// A mask this PEP cannot discharge is a server-side misconfiguration — fail
		// closed (maps to 500) rather than serve an un-masked column.
		return nil, err
	}
	return auditget.Get200JSONResponse{Data: data}, nil
}

// getEntry dispatches the single-entry read to the correct service method:
// cross-tenant (super-admin + admin pool) or tenant-scoped (all others). Extracted
// from Get to keep cognitive complexity ≤ 15 (mirrors executeQuery).
func (a GetAdapter) getEntry(
	ctx context.Context, p *auth.Principal, vr auditVisibilityResult,
	vis tenant.RowVisibility, id string,
) (*ledger.Entry, error) {
	if vr.isCrossTenant {
		// F1: the cross-tenant path ALWAYS requires audit:read from the PDP,
		// independent of and additive to the route-level gate (mirrors executeQuery).
		if err := requireAuditReadForCrossTenant(ctx, p.Subject); err != nil {
			return nil, err
		}
		return a.S.GetByIDCrossTenant(ctx, vr.ctv, id)
	}
	// Tenant axis (#1618): typed tenant scope re-parsed from the authenticated
	// principal (guaranteed non-empty — the empty case is rejected in Get).
	tid, err := tenant.ParseTenantID(p.TenantID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"audit get: principal tenant is not canonical", err)
	}
	return a.S.GetByID(ctx, tid, vis, id)
}

// Handler is the composite route handler for the auditquery slice.
type Handler struct {
	listH *auditlist.Handler
	getH  *auditget.Handler
}

// NewHandler creates an auditquery Handler with the generated list and get
// handlers. The get route uses a flat audit:read gate (see GetAdapter): unlike the
// list's auditQueryPolicy there is no actorId-self exemption.
func NewHandler(svc *Service) *Handler {
	return &Handler{
		listH: auditlist.NewHandler(ListAdapter{svc}, auditQueryPolicy),
		getH:  auditget.NewHandler(GetAdapter{svc}, auth.RequirePermission(authz.PermAuditRead())),
	}
}

// RegisterRoutes mounts the audit list and get contracts on mux.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	if err := h.listH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.getH.RegisterRoutes(mux)
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
	item := &auditlist.ResponseDataItem{
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
	}
	// Only set Payload when redacted bytes are non-empty. An empty []byte stored
	// as json.RawMessage in the any-typed Payload field is non-nil, so ToMap
	// includes it and json.Marshal fails with "unexpected end of JSON input"
	// (#2199). Leaving Payload nil means ToMap omits the key entirely via the
	// `if i.Payload != nil` guard in generated/contracts/http/audit/list/v1/types_gen.go.
	if raw := redaction.RedactPayload(e.Payload); len(raw) > 0 {
		item.Payload = json.RawMessage(raw)
	}
	return item
}

// toGetResponseDataItem converts a ledger.Entry to auditget.ResponseData for
// http.audit.get.v1. It is the single-entry counterpart of toListResponseDataItem
// and applies the SAME field projection, RFC3339Nano timestamp formatting, and
// payload redaction — the field set is identical to the list item (the generated
// DTO types differ per contract, so the converter is duplicated rather than sharing
// a Go type, per the cell-patterns DTO-scope-A rule). See toListResponseDataItem
// for the per-field exposure rationale (SessionID excluded, CorrelationID/TenantID
// surfaced behind the column-masking funnel, etc.).
func toGetResponseDataItem(e *ledger.Entry) *auditget.ResponseData {
	occurredAt := ""
	if !e.OccurredAt.IsZero() {
		occurredAt = e.OccurredAt.Format(time.RFC3339Nano)
	}
	item := &auditget.ResponseData{
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
	}
	// Only set Payload when redacted bytes are non-empty (#2199) — see
	// toListResponseDataItem.
	if raw := redaction.RedactPayload(e.Payload); len(raw) > 0 {
		item.Payload = json.RawMessage(raw)
	}
	return item
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
