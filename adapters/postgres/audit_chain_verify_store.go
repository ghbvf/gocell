package postgres

import (
	"context"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/framework/pkg/ctxcancel"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// Compile-time assertion: AuditChainVerifyStore implements the interface.
var _ ledger.ChainVerifyStore = (*AuditChainVerifyStore)(nil)

// AuditChainVerifyStore is the PostgreSQL ledger.ChainVerifyStore for the #1755
// admin audit chain verify tool. Like AuditCrossTenantStore it is backed by the
// DEDICATED gocell_audit_admin admin read pool (permissive RLS SELECT policy
// USING(true)), so EnumerateChains can SELECT DISTINCT every (namespace, tenant)
// chain — which the NOBYPASSRLS serving role cannot do (RLS hides cross-tenant
// rows). FORCE RLS stays ON for the serving role: cross-tenant visibility comes
// from the explicit pg_policy row, NOT from bypassing RLS (ADR #1676 preserved).
//
// Unlike AuditCrossTenantStore it carries a per-namespace Protocol map: VerifyChain
// re-computes HMACs, and the relay and bootstrap chains have INDEPENDENT HMAC keys,
// so a chain must be verified with its own namespace's protocol. A namespace with
// no registered protocol fails closed (a misconfiguration is not tamper).
//
// It returns only integrity verdicts, never audit row content, so — like
// VerifyBootstrapTailOnStartup — it carries NO tenant.CrossTenantVisibility
// obligation (it is a system-integrity op gated by admin-pool possession, not a
// data-read funnel; see ledger.ChainVerifyStore godoc).
type AuditChainVerifyStore struct {
	db        pgexec.PGExecutor
	protocols map[string]*ledger.Protocol
}

// enumerateChainsSQL returns one row per (namespace, tenant_id) chain with its
// seq_no bounds, in a single grouped scan over the admin pool. The admin role's
// permissive RLS SELECT policy USING(true) returns every tenant's rows, so the
// GROUP BY spans BOTH namespace chains and ALL tenants.
const enumerateChainsSQL = `SELECT namespace, tenant_id, MIN(seq_no), MAX(seq_no)
FROM audit_entries
GROUP BY namespace, tenant_id`

// NewAuditChainVerifyStore builds the admin-pool-backed chain verify store. It
// requires:
//   - a non-nil admin *Pool (the gocell_audit_admin role);
//   - a non-empty protocols map (one *ledger.Protocol per namespace it verifies —
//     in production the relay "auditcore" + "bootstrap" chains), each non-nil.
//
// AI-HARD (fail-closed admin-pool guard, #1755): the constructor runs
// pool.AuditAdminReadyCheck before returning, so a non-admin pool — e.g. the
// NOBYPASSRLS serving pool, which would silently RLS-under-enumerate rather than
// error — cannot yield a usable verify store. (The composition root also preflights
// the same pool for the cross-tenant store; this independent check makes the verify
// store self-guarding regardless of wiring order.)
func NewAuditChainVerifyStore(ctx context.Context, pool *Pool, protocols map[string]*ledger.Protocol) (*AuditChainVerifyStore, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewAuditChainVerifyStore: pool must not be nil")
	}
	if len(protocols) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewAuditChainVerifyStore: at least one namespace protocol is required")
	}
	protos := make(map[string]*ledger.Protocol, len(protocols))
	for ns, p := range protocols {
		if p == nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"postgres.NewAuditChainVerifyStore: nil protocol for namespace",
				errcode.WithInternal(errcode.InternalAttr("namespace", ns)))
		}
		protos[ns] = p
	}
	if err := pool.AuditAdminReadyCheck(ctx); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGSchemaShape,
			"postgres.NewAuditChainVerifyStore: admin pool role preflight failed", err)
	}
	return &AuditChainVerifyStore{db: pgexec.New(pool.DB()), protocols: protos}, nil
}

// EnumerateChains returns one ledger.ChainRef per (namespace, tenant_id) chain via
// the single grouped admin-pool query. Returns an empty (non-nil) slice when the
// table is empty.
func (s *AuditChainVerifyStore) EnumerateChains(ctx context.Context) ([]ledger.ChainRef, error) {
	rows, queryErr := s.db.Query(ctx, enumerateChainsSQL)
	if queryErr != nil {
		return nil, ctxcancel.WrapOrInfra(queryErr, "enumerate_chains", "chain-verify",
			ErrAdapterPGQuery, "audit ledger: enumerate chains query failed")
	}
	defer rows.Close()

	refs := []ledger.ChainRef{}
	for rows.Next() {
		var r ledger.ChainRef
		if scanErr := rows.Scan(&r.Namespace, &r.TenantID, &r.MinSeq, &r.MaxSeq); scanErr != nil {
			return nil, ctxcancel.WrapOrInfra(scanErr, "enumerate_scan", "chain-verify",
				ErrAdapterPGQuery, "audit ledger: enumerate chains scan failed")
		}
		refs = append(refs, r)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, ctxcancel.WrapOrInfra(rowsErr, "enumerate_rows_err", "chain-verify",
			ErrAdapterPGQuery, "audit ledger: enumerate chains iterate failed")
	}
	return refs, nil
}

// VerifyChain re-computes the HMAC chain for [fromSeq, toSeq] of the explicit
// (namespace, tenant) chain using the namespace-matched protocol. A namespace with
// no registered protocol FAILS CLOSED (error, not valid=false) — a missing
// protocol is a misconfiguration, and verifying with the wrong namespace's HMAC
// key would falsely flag every entry as tampered.
func (s *AuditChainVerifyStore) VerifyChain(ctx context.Context, namespace, tenantID string, fromSeq, toSeq int64) (bool, int64, error) {
	proto, ok := s.protocols[namespace]
	if !ok {
		return false, fromSeq, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"audit ledger: no protocol registered for chain namespace",
			errcode.WithInternal(errcode.InternalAttr("namespace", namespace)))
	}
	return verifyChainExec(ctx, s.db, proto, namespace, tenantID, fromSeq, toSeq)
}
