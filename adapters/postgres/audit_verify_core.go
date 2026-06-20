package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/framework/pkg/ctxcancel"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// audit_verify_core.go — the SINGLE PostgreSQL audit-chain verify loop, shared by
// two callers (#1755):
//
//   - the ctx-scoped serving path: (*LedgerStore).Verify, namespace-bound, tenant
//     derived from the request scope (startup tail-verify / conformance);
//   - the explicit-(namespace, tenant) admin path: AuditChainVerifyStore.VerifyChain,
//     which verifies any tenant's chain on the gocell_audit_admin read pool.
//
// Both call verifyChainExec with an injected executor + protocol, so the
// security-critical HMAC recompute + PrevHash linkage logic exists in exactly ONE
// place and cannot drift between the serving and admin backends. The chain is
// scoped entirely by the EXPLICIT (namespace, tenant) SQL predicates (selectRangeSQL
// + the baseline lookup), NOT by any SET LOCAL / RLS GUC — which is precisely why
// running it on the admin pool (permissive USING(true) policy, no GUC) yields
// identical results to the serving pool: the predicate does all the scoping, the
// policy only permits the read.

// verifyChainExec re-computes the HMAC chain for [fromSeq, toSeq] of the explicit
// (namespace, tenant) chain over the supplied executor + protocol. Returns
// valid=true, firstInvalidSeq=-1 when intact; valid=false + the first invalid
// seq_no on tamper; a non-nil error on validation / infra / missing-row failures.
func verifyChainExec(
	ctx context.Context, db pgexec.PGExecutor, proto *ledger.Protocol,
	ns, tenantID string, fromSeq, toSeq int64,
) (valid bool, firstInvalidSeq int64, err error) {
	if fromSeq < 1 || toSeq < fromSeq {
		return false, fromSeq, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: Verify requires 1 <= fromSeq <= toSeq")
	}
	prevHash, baseErr := verifyBaselineExec(ctx, db, ns, tenantID, fromSeq)
	if baseErr != nil {
		return false, fromSeq, baseErr
	}
	return verifyRangeExec(ctx, db, proto, ns, tenantID, fromSeq, toSeq, prevHash)
}

// verifyBaselineExec returns the hash of entries[fromSeq-1] in the (namespace,
// tenant) chain when fromSeq > 1 (the sub-range baseline), or "" when fromSeq == 1
// (chain genesis). A missing baseline row returns ErrAuditLedgerNotFound.
func verifyBaselineExec(ctx context.Context, db pgexec.PGExecutor, ns, tenantID string, fromSeq int64) (string, error) {
	if fromSeq == 1 {
		return "", nil
	}
	var baselineHash string
	err := db.QueryRow(
		ctx,
		`SELECT hash FROM audit_entries WHERE namespace=$1 AND tenant_id=$2 AND seq_no=$3`,
		ns, tenantID, fromSeq-1,
	).Scan(&baselineHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: Verify baseline entry not found",
			errcode.WithDetails(errcode.PublicInt("baselineSeqNo", fromSeq-1)))
	}
	if err != nil {
		return "", ctxcancel.WrapOrInfra(err, "verify_baseline", ns,
			ErrAdapterPGQuery, "audit ledger: verify baseline lookup failed")
	}
	return baselineHash, nil
}

// verifyRangeExec scans entries in [fromSeq, toSeq] of the (namespace, tenant)
// chain and validates gap-freeness, PrevHash linkage, and hash recomputation
// under proto. prevHash is the expected PrevHash of the first scanned entry
// (empty string for the chain genesis). proto's namespace + HMAC key MUST match
// the chain (the AuditChainVerifyStore protocol map guarantees this), else every
// entry reports tampered.
func verifyRangeExec(
	ctx context.Context, db pgexec.PGExecutor, proto *ledger.Protocol,
	ns, tenantID string, fromSeq, toSeq int64, prevHash string,
) (bool, int64, error) {
	rows, queryErr := db.Query(ctx, selectRangeSQL, ns, tenantID, fromSeq, toSeq)
	if queryErr != nil {
		return false, 0, ctxcancel.WrapOrInfra(queryErr, "verify_query", ns,
			ErrAdapterPGQuery, "audit ledger: verify range query failed")
	}
	defer rows.Close()

	expectedSeq := fromSeq
	for rows.Next() {
		var e ledger.Entry
		if scanErr := rows.Scan(
			&e.SeqNo,
			&e.EventID, &e.EventType, &e.ActorID,
			&e.SubjectID, &e.TenantID, &e.SessionID, &e.CorrelationID, &e.TraceID, &e.OccurredAt,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); scanErr != nil {
			return false, 0, ctxcancel.WrapOrInfra(scanErr, "verify_scan", ns,
				ErrAdapterPGQuery, "audit ledger: verify scan failed")
		}
		if e.SeqNo != expectedSeq {
			return false, expectedSeq, nil
		}
		expectedSeq++
		if e.PrevHash != prevHash {
			return false, e.SeqNo, nil
		}
		if e.Hash != proto.ComputeHash(e.PrevHash, &e) {
			return false, e.SeqNo, nil
		}
		prevHash = e.Hash
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return false, 0, ctxcancel.WrapOrInfra(rowsErr, "verify_rows_err", ns,
			ErrAdapterPGQuery, "audit ledger: verify rows error")
	}
	if expectedSeq <= toSeq {
		return false, expectedSeq, errcode.New(
			errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: entry not found during Verify",
			errcode.WithDetails(errcode.PublicInt("missingSeqNo", expectedSeq)),
		)
	}
	return true, -1, nil
}
