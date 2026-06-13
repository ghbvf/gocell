package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Compile-time assertion: AuditCrossTenantStore implements the interface.
var _ ledger.CrossTenantQueryStore = (*AuditCrossTenantStore)(nil)

// AuditCrossTenantStore is the PostgreSQL implementation of
// ledger.CrossTenantQueryStore for super-admin cross-tenant audit reads (#1810).
// It is backed by a DEDICATED admin connection pool (the gocell_audit_admin role),
// which carries a permissive RLS SELECT policy USING(true) so it can read every
// tenant's rows. FORCE RLS stays ON for the serving role — the cross-tenant
// visibility comes from an explicit pg_policy row, not from bypassing RLS.
//
// Unlike LedgerStore, AuditCrossTenantStore:
//   - issues NO namespace predicate (returns rows from BOTH the relay and
//     bootstrap namespace chains in one scan)
//   - issues NO tenant predicate (all tenants are visible via the admin-role policy)
//   - is READ-ONLY: no Append, no RunInTx, no clock
//   - does NOT honor the ordinary serving Store.Query path
//
// The dedicated admin pool must be configured with the gocell_audit_admin role.
// Composition-root wiring is responsible for provisioning that pool; if the pool
// is not configured, callers remain fail-closed at HTTP 501
// (RowScopeAllUnsupportedError) exactly as before #1810.
type AuditCrossTenantStore struct {
	db pgexec.PGExecutor
}

// NewAuditCrossTenantStore constructs an AuditCrossTenantStore backed by the
// supplied admin pool. pool must not be nil. The pool must be configured with
// the gocell_audit_admin role (or equivalent permissive RLS SELECT policy) so
// QueryCrossTenant can read across all tenants without bypassing FORCE RLS.
//
// Package path: github.com/ghbvf/gocell/adapters/postgres.
// Signature:    NewAuditCrossTenantStore(pool *pgxpool.Pool) (*AuditCrossTenantStore, error).
func NewAuditCrossTenantStore(pool *pgxpool.Pool) (*AuditCrossTenantStore, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewAuditCrossTenantStore: pool must not be nil")
	}
	return &AuditCrossTenantStore{db: pgexec.New(pool)}, nil
}

// crossTenantBaseSQL is the full query preamble including the always-present
// `WHERE true` anchor. No namespace predicate and no tenant_id predicate are
// applied — the admin pool's permissive RLS SELECT policy USING(true) returns
// every row from every tenant and both namespace chains.
//
// The `WHERE true` anchor guarantees that every subsequent predicate can
// unconditionally use the `AND <col>` prefix (AppendKeyset's keyset WHERE clause
// always uses "AND" so it requires a prior WHERE to be syntactically valid; the
// first-page / no-filter case produces no keyset predicate, so the anchor is
// harmless then too).
//
// ORDER BY timestamp DESC, id ASC matches QuerySort() and is served by
// idx_audit_namespace_ts_id without a sort step.
const crossTenantBaseSQL = `SELECT id, seq_no, event_id, event_type, actor_id,
       subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
       timestamp, payload, prev_hash, hash
FROM audit_entries
WHERE true`

// QueryCrossTenant lists audit entries across ALL tenants and BOTH namespace
// chains matching AuditFilters, using keyset cursor pagination. params.Sort must
// be non-empty (callers pass ledger.QuerySort). Returns up to params.FetchLimit()
// rows for N+1 hasMore detection. Returns an empty (non-nil) slice when no
// entries match.
//
// The ctv parameter carries the sealed RowScopeAll obligation; it is accepted to
// enforce the typed-funnel invariant (#1760) — only callers routed through the
// sealed CrossTenantVisibility minter can call this method. The owner dimension
// is unrestricted (RowScopeAll), so AuditFilters is the only narrowing applied.
//
// NO SET LOCAL, NO RunInTx, NO Protocol — this is a pure read-only path.
func (s *AuditCrossTenantStore) QueryCrossTenant(
	ctx context.Context,
	_ tenant.CrossTenantVisibility,
	filters ledger.AuditFilters,
	params query.ListParams,
) ([]*ledger.Entry, error) {
	if len(params.Sort) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: cross-tenant query requires a non-empty sort")
	}

	var err error
	params, err = bindTimestampCursor(params)
	if err != nil {
		return nil, err
	}

	b := pgquery.NewBuilder()
	b.Append(crossTenantBaseSQL)
	// Optional filter predicates — each uses "AND" prefix since crossTenantBaseSQL
	// ends with "WHERE true", so the SQL is always syntactically valid.
	b.AppendIf(filters.EventType != "", `AND event_type = `, filters.EventType)
	b.AppendIf(filters.ActorID != "", `AND actor_id = `, filters.ActorID)
	b.AppendIf(filters.SubjectID != "", `AND subject_id = `, filters.SubjectID)
	b.AppendIf(filters.TraceID != "", `AND trace_id = `, filters.TraceID)
	b.AppendIf(!filters.From.IsZero(), `AND timestamp >= `, filters.From)
	b.AppendIf(!filters.To.IsZero(), `AND timestamp <= `, filters.To)

	if ksErr := pgquery.AppendKeyset(b, params); ksErr != nil {
		return nil, ksErr
	}

	sql, args := b.Build()
	rows, queryErr := s.db.Query(ctx, sql, args...)
	if queryErr != nil {
		return nil, ctxcancel.WrapOrInfra(queryErr, "cross_tenant_query", "cross-tenant",
			ErrAdapterPGQuery, "audit ledger: cross-tenant query failed")
	}
	defer rows.Close()

	result, scanErr := scanAuditCrossTenantRows(rows)
	if scanErr != nil {
		return nil, scanErr
	}
	if result == nil {
		result = []*ledger.Entry{}
	}
	return result, nil
}

// scanAuditCrossTenantRows scans all rows from a pgx.Rows result into
// []*ledger.Entry. Separate from LedgerStore.scanEntries (a method that carries
// a namespace string) because cross-tenant reads span all namespaces and need
// no namespace context for error attribution.
func scanAuditCrossTenantRows(rows pgx.Rows) ([]*ledger.Entry, error) {
	var entries []*ledger.Entry
	for rows.Next() {
		var e ledger.Entry
		if err := rows.Scan(
			&e.ID, &e.SeqNo,
			&e.EventID, &e.EventType, &e.ActorID,
			&e.SubjectID, &e.TenantID, &e.SessionID, &e.CorrelationID, &e.TraceID, &e.OccurredAt,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); err != nil {
			return nil, ctxcancel.WrapOrInfra(err, "scan", "cross-tenant",
				ErrAdapterPGQuery, "audit ledger: cross-tenant scan entry failed")
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "rows_err", "cross-tenant",
			ErrAdapterPGQuery, "audit ledger: cross-tenant iterate entries failed")
	}
	return entries, nil
}

// crossTenantSQLForTest rebuilds the SQL for a given filter set + params without
// executing. Used by unit tests to verify that the generated SQL contains no
// namespace or tenant_id predicates, confirming the predicate-free design. Must
// not be called from production paths.
func crossTenantSQLForTest(filters ledger.AuditFilters, params query.ListParams) (string, error) {
	b := pgquery.NewBuilder()
	b.Append(crossTenantBaseSQL)
	b.AppendIf(filters.EventType != "", `AND event_type = `, filters.EventType)
	b.AppendIf(filters.ActorID != "", `AND actor_id = `, filters.ActorID)
	b.AppendIf(filters.SubjectID != "", `AND subject_id = `, filters.SubjectID)
	b.AppendIf(filters.TraceID != "", `AND trace_id = `, filters.TraceID)
	b.AppendIf(!filters.From.IsZero(), `AND timestamp >= `, filters.From)
	b.AppendIf(!filters.To.IsZero(), `AND timestamp <= `, filters.To)
	if err := pgquery.AppendKeyset(b, params); err != nil {
		return "", err
	}
	sql, _ := b.Build()
	return sql, nil
}

// crossTenantSQLHasNoTenantPredicate reports whether the SQL produced by the
// cross-tenant builder contains no namespace or tenant_id predicates.
func crossTenantSQLHasNoTenantPredicate(sql string) bool {
	lower := strings.ToLower(sql)
	return !strings.Contains(lower, "namespace =") &&
		!strings.Contains(lower, "tenant_id =")
}
