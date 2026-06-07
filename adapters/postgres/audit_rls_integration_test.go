//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// #1618 audit_entries FORCE ROW LEVEL SECURITY integration tests. Like the
// config-table RLS tests (rls_force_integration_test.go) these exercise the real
// DB-kernel isolation through a restricted (NOSUPERUSER, NON-owner, NOBYPASSRLS)
// role — RLS is invisible to the container superuser. They INSERT raw rows
// (bypassing the appender) so the assertions isolate the DB policy itself:
//
//   - per-(namespace, tenant) chains: two tenants both genesis seq_no=1 in the
//     SAME namespace coexist (UNIQUE(namespace, tenant_id, seq_no)).
//   - RLS USING: a tenant sees its own rows + tenant-less system rows, never
//     another tenant's rows.
//   - RLS WITH CHECK: a scoped writer cannot INSERT another tenant's row (42501).
//   - the `OR tenant_id = ''` system-rows clause: a GUC-unset writer can INSERT a
//     tenant_id='' row, and an unscoped reader sees ONLY system rows.
//
// Not parallel: restrictedAppPool provisions a cluster-global role.

// auditHash64 is a syntactically valid 64-hex hash satisfying ck_audit_hash_format
// (the CHECK validates format, not chain correctness, for these raw RLS inserts).
const auditHash64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const insertAuditRowSQL = `
INSERT INTO audit_entries
    (id, namespace, seq_no, event_id, event_type, actor_id,
     subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
     timestamp, payload, prev_hash, hash)
VALUES ($1, $2, $3, $4, $5, $6, '', $7, '', '', '', $8, $8, '{}', $9, $10)`

// auditRow holds the variable fields of a raw audit_entries INSERT.
type auditRow struct {
	id, namespace, eventID, actorID, tenantID string
	seqNo                                      int64
}

// genesisAuditRow builds a seq_no=1 genesis row (prev_hash='') for the given chain.
func genesisAuditRow(id, ns, eventID, actorID, tenantID string) auditRow {
	return auditRow{id: id, namespace: ns, eventID: eventID, actorID: actorID, tenantID: tenantID, seqNo: 1}
}

// execAuditInsert runs one raw INSERT on the ambient tx. prevHash is "" for
// genesis (seq_no=1) and a 64-hex string otherwise (ck_audit_hash_format).
func execAuditInsert(ctx context.Context, r auditRow) error {
	tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
	prevHash := ""
	if r.seqNo > 1 {
		prevHash = auditHash64
	}
	now := time.Unix(1700000000, int64(r.seqNo)).UTC()
	_, err := tx.Exec(ctx, insertAuditRowSQL,
		r.id, r.namespace, r.seqNo, r.eventID, "rls.test", r.actorID,
		r.tenantID, now, prevHash, auditHash64)
	return err
}

// scopedInsertAudit inserts r under tenant tid's RLS scope (GUC = tid). Pass an
// empty tid to run GUC-unset (the system-row / pre-auth appender path).
func scopedInsertAudit(t *testing.T, tm *TxManager, tid tenant.TenantID, r auditRow) error {
	t.Helper()
	ctx := context.Background()
	if tid != "" {
		ctx = tenant.WithScope(ctx, tid)
	}
	return tm.RunInTx(ctx, func(ctx context.Context) error { return execAuditInsert(ctx, r) })
}

// scopedCountAudit counts audit_entries in namespace ns VISIBLE under tenant
// tid's RLS scope. Pass an empty tid for the GUC-unset (unscoped) read. The WHERE
// omits tenant_id — RLS supplies the tenant predicate, so this measures
// DB-enforced isolation.
func scopedCountAudit(t *testing.T, tm *TxManager, tid tenant.TenantID, ns string) int {
	t.Helper()
	ctx := context.Background()
	if tid != "" {
		ctx = tenant.WithScope(ctx, tid)
	}
	var n int
	err := tm.RunInTx(ctx, func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE namespace = $1`, ns).Scan(&n)
	})
	require.NoError(t, err)
	return n
}

func TestAuditRLS_PerTenantChainsAndIsolation(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	const ns = "auditcore"

	// Two tenants both genesis seq_no=1 in the SAME namespace — the per-(namespace,
	// tenant) UNIQUE allows the duplicate seq_no across tenants.
	require.NoError(t, scopedInsertAudit(t, tm, rlsTenantA,
		genesisAuditRow("11111111-1111-1111-1111-111111111111", ns, "ev-a1", "actor-a", string(rlsTenantA))))
	require.NoError(t, scopedInsertAudit(t, tm, rlsTenantB,
		genesisAuditRow("22222222-2222-2222-2222-222222222222", ns, "ev-b1", "actor-b", string(rlsTenantB))))
	// A tenant-less system row (GUC unset → admitted by the `OR tenant_id = ''`
	// WITH CHECK clause). genesis seq_no=1 in the "" sub-chain.
	require.NoError(t, scopedInsertAudit(t, tm, "",
		genesisAuditRow("33333333-3333-3333-3333-333333333333", ns, "ev-sys", "actor-sys", "")))

	// Tenant A: own row + system row, NEVER tenant B's row (RLS USING).
	assert.Equal(t, 2, scopedCountAudit(t, tm, rlsTenantA, ns),
		"tenant A must see its own row + the system row, not tenant B's")
	// Tenant B: own row + system row, NEVER tenant A's row.
	assert.Equal(t, 2, scopedCountAudit(t, tm, rlsTenantB, ns),
		"tenant B must see its own row + the system row, not tenant A's")
	// Unscoped (GUC unset): the predicate collapses to tenant_id='' → ONLY the
	// system row is visible (fail-closed; never any tenant's rows).
	assert.Equal(t, 1, scopedCountAudit(t, tm, "", ns),
		"an unscoped reader must see ONLY tenant-less system rows")
}

func TestAuditRLS_WithCheckRejectsCrossTenant(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	// Scope = A, but the row claims tenant_id = B → WITH CHECK must reject.
	err := scopedInsertAudit(t, tm, rlsTenantA,
		genesisAuditRow("44444444-4444-4444-4444-444444444444", "auditcore", "ev-wc", "actor", string(rlsTenantB)))
	require.Error(t, err, "writing an audit row for another tenant must be rejected by WITH CHECK")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42501", pgErr.Code, "expected row-level-security WITH CHECK violation (SQLSTATE 42501)")
}

func TestAuditRLS_SchemaGuardVerifiesAuditTable(t *testing.T) {
	// verifyRLS iterates expectedRLSTables (now incl. audit_entries with the
	// SystemRowsReadable variant); a real migrated DB must pass the shape check —
	// proving the migration-055 `OR tenant_id = ''` policy renders to a predicate
	// that rlsTenantWithSystemPredicateRe accepts on the CI PG version.
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	require.NoError(t, verifyRLS(context.Background(), admin),
		"verifyRLS must accept the migration-055 audit_entries OR tenant_id='' policy shape")
}
