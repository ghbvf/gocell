// Package auditquery implements the audit-query slice: query audit entries
// via HTTP using ledger.Store.
package auditquery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Service implements audit query business logic against a ledger.QueryStore.
//
// The narrow QueryStore interface (just Query, no Append/Tail/Verify/...) lets
// composition roots inject a read-side aggregator like ledger.MultiStore that
// fans out reads across multiple chains (issue #1121 / ADR 202605270230 —
// auditcore relay chain + bootstrap chain). Any concrete ledger.Store also
// satisfies QueryStore by structural typing, so single-chain deployments need
// no extra wiring.
type Service struct {
	store ledger.QueryStore  `gocell:"required"`
	codec *query.CursorCodec `gocell:"required" gocellCode:"ErrCellMissingCodec" gocellErr:"auditquery: cursor codec is required"`
	// txRunner wraps each page fetch in a tenant-scoped RunInTx so the FORCE RLS
	// app.tenant_id GUC (set from the post-auth ctxkeys.TenantID via
	// tenantScopeForTx) is active on the read connection (#1618). No
	// tenant.WithScope is used — the post-auth principal tenant flows through the
	// ctxkeys fallback. In demo/mem mode RunInTx is a passthrough.
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"auditquery: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger   *slog.Logger
	runMode  query.RunMode

	// crossTenantStore is the OPTIONAL admin-pool-backed store for super-admin
	// cross-tenant reads (#1810). When nil, QueryCrossTenant returns
	// RowScopeAllUnsupportedError (HTTP 501, fail-closed). No gocell:"required" tag
	// because absence is a valid operational state: the admin pool creds may not be
	// provisioned, in which case super-admin reads stay gracefully unavailable.
	crossTenantStore ledger.CrossTenantQueryStore
}

// ServiceOption is a functional option for NewService.
type ServiceOption func(*Service)

// WithCrossTenantStore injects the optional CrossTenantQueryStore. When not
// supplied (or when a nil or typed-nil interface is passed), QueryCrossTenant
// returns RowScopeAllUnsupportedError (HTTP 501) — graceful fail-closed, never
// fail-open. Do NOT carry gocell:"required" here: the admin read pool
// creds are an optional operational capability.
//
// Uses validation.IsNilInterface (F5, Codex review): a plain `s == nil` check
// misses typed-nil values (e.g. (*MemCrossTenantStore)(nil)), which would be
// stored and then nil-panic on the first QueryCrossTenant call, bypassing the
// 501 fail-closed contract.
func WithCrossTenantStore(s ledger.CrossTenantQueryStore) ServiceOption {
	return func(svc *Service) {
		if validation.IsNilInterface(s) {
			return
		}
		svc.crossTenantStore = s
	}
}

// NewService creates an audit-query Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// store, codec and txRunner must be non-nil; codec is required for pagination,
// txRunner for the tenant-scoped RunInTx that activates FORCE RLS on reads.
// opts are optional ServiceOption values; currently WithCrossTenantStore is the
// only supported option.
func NewService(
	store ledger.QueryStore, codec *query.CursorCodec, logger *slog.Logger,
	txRunner persistence.CellTxManager, runMode query.RunMode,
	opts ...ServiceOption,
) (*Service, error) {
	s := &Service{store: store, codec: codec, txRunner: txRunner, logger: logger, runMode: runMode}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Query returns a paginated page of audit entries within tenant t matching the
// given filters. t is the tenant axis (#1618), passed from the authenticated
// principal by the handler; vis is the row-visibility obligation enforced on the
// actor_id owner column by the underlying store. Each page fetch runs inside a
// tenant-scoped RunInTx so DB-layer FORCE RLS is active.
func (s *Service) Query(
	ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, filters ledger.AuditFilters, pageReq query.PageParams,
) (query.PageResult[*ledger.Entry], error) {
	// Post-auth hard boundary (#1618 F2): every audit query is tenant-scoped. The
	// handler derives t from the authenticated principal and the empty case is
	// already rejected there (403), so a non-canonical / empty t reaching here is a
	// server-side invariant break — reject fail-closed rather than let the store's
	// empty-t = system-chain semantics (a trusted internal-only capability) serve a
	// user request. Unlike the store's ValidateQueryTenant (which permits empty for
	// internal system-chain reads), this is the full t.Validate() — empty is invalid
	// at the user-facing boundary.
	if err := t.Validate(); err != nil {
		return query.PageResult[*ledger.Entry]{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"audit query: tenant scope is not canonical", err)
	}
	attrs := []string{"endpoint", "audit-query"}
	if filters.EventType != "" {
		attrs = append(attrs, "eventType", filters.EventType)
	}
	if filters.ActorID != "" {
		attrs = append(attrs, "actorId", filters.ActorID)
	}
	if filters.SubjectID != "" {
		// subjectId participates in the cursor scope fingerprint exactly like
		// actorId/eventType: it changes which rows are paged, so a cursor minted
		// for one subjectId must not silently resume against another (#1290).
		attrs = append(attrs, "subjectId", filters.SubjectID)
	}
	if filters.TraceID != "" {
		// traceId participates in the cursor scope fingerprint exactly like
		// subjectId/actorId: a cursor minted for one traceId must not silently
		// resume against a different traceId or an unfiltered query (#1048).
		attrs = append(attrs, "traceId", filters.TraceID)
	}
	if t.String() != "" {
		// tenant is the primary isolation axis (#1618): a cursor minted under
		// tenant A must not silently resume under tenant B. Including it in the
		// scope fingerprint makes cross-tenant cursor replay produce a
		// "query context mismatch" error rather than silently paging the wrong
		// tenant's rows (#1337 PR-2a review U4). Sourced from the typed t param.
		attrs = append(attrs, "tenantId", t.String())
	}
	// The row-visibility obligation is a cursor-scope axis exactly like tenantId
	// (#1337 PR-4): it decides which actor's rows are paged (via the actor_id
	// owner predicate). A cursor minted under one obligation must not silently
	// resume under another — e.g. if the caller's role changes mid-pagination
	// (self → tenant or vice versa), cursor replay must produce a "query context
	// mismatch" rather than page the wrong owner set. Subject only varies the
	// scope for self/device (empty for tenant), so both axes participate.
	attrs = append(attrs, "rowScope", vis.Scope().String())
	if vis.Subject() != "" {
		attrs = append(attrs, "rowSubject", vis.Subject())
	}
	// From/To are time-range filter predicates, not cursor-scope identifiers.
	// Including zero-value time.Time in the scope would embed "0001-01-01T00:00:00Z"
	// and break cursor reuse when callers omit From/To (F-07). Non-zero values
	// are intentionally excluded from scope as well: time-range filters narrow
	// results within a stable data type; changing From/To does not change the
	// kind of data being paged, so they must not participate in scope fingerprinting.
	qctx := query.QueryContext(attrs...)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*ledger.Entry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       ledger.QuerySort(),
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*ledger.Entry, error) {
			// Keyset pagination is pushed into the store (PG: SQL keyset via
			// pgquery.AppendKeyset over idx_audit_namespace_ts_id; MemStore:
			// in-memory keyset). The store returns up to params.FetchLimit()
			// (Limit+1) rows; BuildPageResult trims to Limit and detects hasMore.
			//
			// The store read runs inside RunInTx so the FORCE RLS app.tenant_id GUC
			// is set from the post-auth ctxkeys.TenantID (#1618); MultiStore fans
			// the read out to every backing PG store on the same ambient tx, so one
			// RunInTx scopes them all. Demo/mem RunInTx is a passthrough.
			var entries []*ledger.Entry
			err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
				page, qerr := s.store.Query(txCtx, t, vis, filters, params)
				if qerr != nil {
					return qerr
				}
				entries = page
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("audit-query: query: %w", err)
			}
			return entries, nil
		},
		Extract: func(e *ledger.Entry) []any {
			return []any{e.Timestamp.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "auditquery"),
		RunMode:     s.runMode,
	})
}

// QueryCrossTenant returns a paginated page of audit entries across ALL tenants,
// using the admin-pool-backed CrossTenantQueryStore (#1810). This method is the
// exclusive path for super-admin cross-tenant reads; it bypasses the per-tenant
// RunInTx wrapper because the cross-tenant store uses its own admin pool with a
// role-scoped permissive RLS policy (gocell_audit_admin), not the FORCE RLS
// serving pool.
//
// Fail-closed optionality: when crossTenantStore is nil (admin pool creds not
// provisioned), returns RowScopeAllUnsupportedError (HTTP 501). This preserves
// the pre-#1810 behavior — graceful unavailability, never fail-open.
//
// ctv carries the sealed CrossTenantVisibility obligation; it is the sole
// permitted mint of RowScopeAll and proves the mandatory FR-007 audit has
// already been emitted by (*auth.Principal).CrossTenantVisibility.
// errMsgInvalidCrossTenantObligation is the const-literal PEP fail-close message
// (MESSAGE-CONST-LITERAL-01): a zero-value or non-All CrossTenantVisibility must
// never reach the store (F2, Codex review). The typed funnel guarantees a
// CrossTenantVisibility is passed (forget = compile error), but the zero value is
// constructable, so the PEP validates the obligation at runtime.
const errMsgInvalidCrossTenantObligation = "cross-tenant audit read: invalid or zero CrossTenantVisibility obligation"

func (s *Service) QueryCrossTenant(
	ctx context.Context, ctv tenant.CrossTenantVisibility, filters ledger.AuditFilters, pageReq query.PageParams,
) (query.PageResult[*ledger.Entry], error) {
	if s.crossTenantStore == nil {
		return query.PageResult[*ledger.Entry]{}, ledger.RowScopeAllUnsupportedError()
	}
	// PEP: validate the obligation before reading. The typed funnel is Hard
	// (forget/forge = compile error), but Go's zero value (tenant.CrossTenantVisibility{})
	// is constructable, so this guard fail-closes a zero/invalid obligation (F2).
	// ctv.Validate is the single-source predicate every store PEP also calls
	// (defense in depth at the data layer).
	if err := ctv.Validate(); err != nil {
		return query.PageResult[*ledger.Entry]{}, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			errMsgInvalidCrossTenantObligation)
	}
	// Cursor-scope fingerprint for cross-tenant reads: same filter axes as Query
	// (eventType/actorId/subjectId/traceId) but WITHOUT tenantId (the read spans
	// all tenants by construction). rowScope is always "all" for this path.
	attrs := []string{"endpoint", "audit-query"}
	if filters.EventType != "" {
		attrs = append(attrs, "eventType", filters.EventType)
	}
	if filters.ActorID != "" {
		attrs = append(attrs, "actorId", filters.ActorID)
	}
	if filters.SubjectID != "" {
		attrs = append(attrs, "subjectId", filters.SubjectID)
	}
	if filters.TraceID != "" {
		attrs = append(attrs, "traceId", filters.TraceID)
	}
	// rowScope is always "all" for cross-tenant reads. No tenantId axis: the
	// cursor is cross-tenant by design, and including a tenantId would be
	// misleading (the read spans every tenant, not one).
	attrs = append(attrs, "rowScope", ctv.Visibility().Scope().String())
	qctx := query.QueryContext(attrs...)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*ledger.Entry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       ledger.QuerySort(),
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*ledger.Entry, error) {
			// No RunInTx wrapper: the cross-tenant store uses its own admin pool
			// (gocell_audit_admin role + role-scoped permissive RLS policy), so the
			// per-tenant FORCE RLS GUC mechanism does not apply here.
			return s.crossTenantStore.QueryCrossTenant(ctx, ctv, filters, params)
		},
		Extract: func(e *ledger.Entry) []any {
			return []any{e.Timestamp.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "auditquery"),
		RunMode:     s.runMode,
	})
}
