//go:build integration

package postgres

// #1810 AuditCrossTenantStore live-PG integration tests.
//
// These tests exercise the real gocell_audit_admin role + the permissive
// audit_admin_read_all RLS SELECT policy (USING(true)) added by migration 065
// against a running PostgreSQL instance provisioned by the shared test container.
//
// Each test provisions the role via the superuser pool and applies migration 065;
// clean-up drops the role at the end so sibling tests do not see it.
//
// Three tests:
//
//  1. TestAuditCrossTenantStore_PG_ReadsAcrossTenants — proves the admin role
//     can SELECT rows from multiple tenants, multiple namespaces, and system
//     rows (tenant_id='') in a single predicate-free scan.
//
//  2. TestAuditCrossTenant_ServingRole_CannotReadCrossTenant — the KEY security
//     assertion: proves the restricted gocell_app role (NOBYPASSRLS) still sees
//     only the GUC-scoped tenant's rows after migration 065; the new permissive
//     policy is scoped TO gocell_audit_admin and does NOT widen gocell_app.
//
//  3. TestAuditCrossTenantStore_PG_Conformance — wires the PG backend into the
//     shared storetest.RunCrossTenantQueryConformance suite.
//
// Run (no live PG required for compilation):
//
//	go test -tags=integration ./adapters/postgres/ -run 'CrossTenant'
//
// Not parallel: role provisioning is cluster-global.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger/storetest"
)

// ctDelta4s is a 4-second row timestamp delta used in cross-tenant seed data.
// No exact mechanical alias exists in testtime for 4s.
const ctDelta4s = 4 * time.Second

// auditAdminPass is the password used when creating gocell_audit_admin in
// these tests. Must match the value used in schema_guard_integration_test.go
// (TestMigration065_SchemaGuard_RolePresent) and deploy/postgres/init/10-restricted-role.sh.
// auditAdminRole is already declared as a package-level const in schema_guard.go.
const auditAdminPass = "test-audit-admin-pw"

// Tenant / namespace fixtures used by the integration tests.
// Must be canonical UUIDs (#1618 rule: non-empty tenants must be RFC-4122 UUIDs).
const (
	ctTenantA = "aa000000-0000-0000-0000-000000000001"
	ctTenantB = "bb000000-0000-0000-0000-000000000002"

	ctNSAuditcore = "auditcore"
	ctNSBootstrap = "bootstrap"
)

// provisionAuditAdminPool creates the gocell_audit_admin role (NOSUPERUSER,
// NOBYPASSRLS, LOGIN) via the superuser admin pool, re-applies migration 065
// so the IF EXISTS guard fires, GRANTs SELECT on audit_entries, and returns a
// Pool connected AS gocell_audit_admin. Cleanup drops the role after the test.
//
// The re-migration step is idempotent (migration 065 uses IF EXISTS) and
// ensures the policy exists whether or not the shared template was built with
// the role present.
func provisionAuditAdminPool(t *testing.T, dsn string, admin *Pool) *Pool {
	t.Helper()
	ctx := context.Background()

	// 1. Create the role (idempotent: DO $$ block checks pg_roles).
	_, err := admin.DB().Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '`+auditAdminRole+`') THEN
				CREATE ROLE `+auditAdminRole+` LOGIN PASSWORD '`+auditAdminPass+`' NOSUPERUSER NOBYPASSRLS;
			END IF;
		END
		$$`)
	require.NoError(t, err, "create gocell_audit_admin role")

	// Drop the role after the test via the shared helper, which drops the dependent
	// audit_admin_read_all policy + revokes grants (DROP OWNED BY) BEFORE DROP ROLE
	// so the cluster-global role does not leak into sibling tests (see
	// dropAuditAdminRole godoc in schema_guard_integration_test.go).
	t.Cleanup(func() { dropAuditAdminRole(admin) })

	// 2. Create the audit_admin_read_all policy directly (mirrors migration 065's
	// Up body). We must NOT re-run migration 065 via goose here: the shared
	// template clone already has all migrations applied, and 065 was a no-op at
	// template-build time (the gocell_audit_admin role did not yet exist, so its
	// `IF EXISTS (pg_roles)` guard skipped). goose will not re-run an already-
	// applied migration, and replaying from a fresh tracking table would instead
	// re-apply migrations 1..63 onto the already-migrated schema and fail
	// ("column ... does not exist"). So we execute the policy DDL directly now
	// that the role exists — idempotent via DROP POLICY IF EXISTS.
	_, polErr := admin.DB().Exec(ctx, `DROP POLICY IF EXISTS audit_admin_read_all ON audit_entries`)
	require.NoError(t, polErr, "drop stale audit_admin_read_all policy")
	_, polErr = admin.DB().Exec(ctx,
		`CREATE POLICY audit_admin_read_all ON audit_entries FOR SELECT TO `+auditAdminRole+` USING (true)`)
	require.NoError(t, polErr, "create audit_admin_read_all policy")

	// 3. Grant schema usage + SELECT so the admin role can read audit_entries.
	grants := []string{
		`GRANT USAGE ON SCHEMA public TO ` + auditAdminRole,
		`GRANT SELECT ON audit_entries TO ` + auditAdminRole,
	}
	for _, g := range grants {
		_, gErr := admin.DB().Exec(ctx, g)
		require.NoError(t, gErr, "grant to gocell_audit_admin: %s", g)
	}

	// 4. Open a pool connected AS gocell_audit_admin.
	adminPool, poolErr := NewPool(ctx, Config{
		DSN: swapUserInDSN(t, dsn, auditAdminRole, auditAdminPass),
	})
	require.NoError(t, poolErr, "open gocell_audit_admin pool")
	t.Cleanup(func() { _ = adminPool.Close(context.Background()) })
	return adminPool
}

// insertAuditRowAsOwner inserts a raw audit_entries row via the superuser
// (owner) pool with an explicit namespace and tenant_id. This bypasses the
// appender protocol so we can precisely control namespace and tenant for
// cross-tenant isolation assertions.
//
// The chain invariants (prev_hash = "" for seq_no=1, a 64-hex hash otherwise)
// satisfy ck_audit_hash_format without needing the full HMAC computation;
// these tests assert query-plane isolation, not chain correctness.
func insertAuditRowAsOwner(
	t *testing.T,
	ownerPool *Pool,
	id, namespace, eventID, actorID, tenantID string,
	seqNo int64,
	ts time.Time,
) {
	t.Helper()
	ctx := context.Background()
	prevHash := ""
	if seqNo > 1 {
		prevHash = auditHash64
	}
	// audit_entries.id is a uuid column; the logical `id` (e.g. "ct-a1-ac") is a
	// readable test handle, so map it to a deterministic valid UUID. event_id is a
	// text column and keeps its logical value.
	_, err := ownerPool.DB().Exec(ctx, insertAuditRowSQL,
		auditRowUUID(id), namespace, seqNo, eventID, "ct.integration", actorID,
		tenantID, ts, prevHash, auditHash64)
	require.NoError(t, err, "insertAuditRowAsOwner id=%s ns=%s tenant=%s", id, namespace, tenantID)
}

// auditRowUUID maps a human-readable test handle (e.g. "ct-a1-ac") to a stable,
// valid UUID for the audit_entries.id column. Deterministic (same handle → same
// UUID) and collision-free across distinct handles, so UNIQUE(id) + the uuid
// column type are both satisfied while the test tables keep readable ids.
func auditRowUUID(logical string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(logical)).String()
}

// insertCrossTenantRowSQL inserts an audit_entries row carrying an explicit
// subject_id ($7) and trace_id ($9) — unlike insertAuditRowSQL which hardcodes
// them empty. The cross-tenant conformance SubjectID/TraceID filter sub-tests
// need real values persisted so QueryCrossTenant's predicate matching is
// exercised on PG. session_id / correlation_id stay empty; occurred_at and
// timestamp both bind $10; payload is an empty JSON object.
const insertCrossTenantRowSQL = `
INSERT INTO audit_entries
    (id, namespace, seq_no, event_id, event_type, actor_id,
     subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
     timestamp, payload, prev_hash, hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '', '', $9, $10, $10, '{}', $11, $12)`

// TestAuditCrossTenantStore_PG_ReadsAcrossTenants proves that a pool connected
// as gocell_audit_admin — backed by the permissive audit_admin_read_all RLS
// SELECT policy (USING(true)) created by migration 065 — can read rows across:
//
//   - two distinct canonical tenants (ctTenantA, ctTenantB)
//   - both the auditcore and bootstrap namespace chains
//   - system rows (tenant_id=”, inserted without a GUC scope)
//
// It also asserts:
//   - timestamp DESC ordering across the cross-tenant result set
//   - keyset pagination across a page boundary (N+1 fetch strategy)
func TestAuditCrossTenantStore_PG_ReadsAcrossTenants(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	ownerPool := openPerTestPool(t, dsn)
	adminPool := provisionAuditAdminPool(t, dsn, ownerPool)

	// Seed rows spread across tenants, namespaces, and system chain.
	// Timestamps are 1-second apart so ordering is deterministic.
	base := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		id, ns, eventID, actorID, tenantID string
		seqNo                              int64
		delta                              time.Duration
	}{
		// auditcore namespace — tenant A
		{"ct-a1-ac", ctNSAuditcore, "ev-a1-ac", "actor-a", ctTenantA, 1, 0},
		// auditcore namespace — tenant B
		{"ct-b1-ac", ctNSAuditcore, "ev-b1-ac", "actor-b", ctTenantB, 1, time.Second},
		// bootstrap namespace — tenant A
		{"ct-a1-bs", ctNSBootstrap, "ev-a1-bs", "actor-a", ctTenantA, 1, testtime.D2s},
		// bootstrap namespace — tenant B
		{"ct-b1-bs", ctNSBootstrap, "ev-b1-bs", "actor-b", ctTenantB, 1, testtime.D3s},
		// system row — tenant_id='', bootstrap namespace, no GUC needed (owner insert)
		{"ct-sys-bs", ctNSBootstrap, "ev-sys-bs", "actor-sys", "", 1, ctDelta4s},
	}
	for _, r := range rows {
		insertAuditRowAsOwner(t, ownerPool, r.id, r.ns, r.eventID, r.actorID, r.tenantID, r.seqNo, base.Add(r.delta))
	}

	// Construct the store backed by the admin-role pool.
	store, err := NewAuditCrossTenantStore(adminPool.DB())
	require.NoError(t, err, "NewAuditCrossTenantStore")

	ctv := tenant.NewCrossTenantVisibility()
	ctx := context.Background()

	// --- Full result set (page limit > total rows) ---
	all, qErr := store.QueryCrossTenant(ctx, ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	require.NoError(t, qErr, "QueryCrossTenant (full page)")

	// All 5 rows must be visible.
	require.Len(t, all, 5, "admin role must see all 5 seeded rows across tenants and namespaces")

	// Both tenants and both namespaces must appear.
	tenantsSeen := make(map[string]bool)
	for _, e := range all {
		tenantsSeen[e.TenantID] = true
	}
	// The system row has TenantID="" (empty); both canonical tenants must appear.
	assert.True(t, tenantsSeen[ctTenantA], "tenant A rows must be visible to admin role")
	assert.True(t, tenantsSeen[ctTenantB], "tenant B rows must be visible to admin role")
	assert.True(t, tenantsSeen[""], "system (tenant_id='') rows must be visible to admin role")

	// Verify event IDs from both namespaces are present. Namespace is not a
	// direct ledger.Entry field; we assert coverage via the event IDs that were
	// seeded in specific namespace chains (auditcore and bootstrap respectively).
	eventIDs := make(map[string]bool)
	for _, e := range all {
		eventIDs[e.EventID] = true
	}
	assert.True(t, eventIDs["ev-a1-ac"], "auditcore-namespace tenant-A row must be present")
	assert.True(t, eventIDs["ev-b1-bs"], "bootstrap-namespace tenant-B row must be present")
	assert.True(t, eventIDs["ev-sys-bs"], "system row must be present")

	// Both namespace chains (auditcore + bootstrap) must be represented.
	// ev-a1-ac / ev-b1-ac come from the auditcore namespace;
	// ev-a1-bs / ev-b1-bs / ev-sys-bs come from the bootstrap namespace.
	hasAuditcore := eventIDs["ev-a1-ac"] || eventIDs["ev-b1-ac"]
	hasBootstrap := eventIDs["ev-a1-bs"] || eventIDs["ev-b1-bs"] || eventIDs["ev-sys-bs"]
	assert.True(t, hasAuditcore, "admin role must see rows from the auditcore namespace")
	assert.True(t, hasBootstrap, "admin role must see rows from the bootstrap namespace")

	// Results must be ordered timestamp DESC, id ASC.
	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1], all[i]
		if cur.Timestamp.After(prev.Timestamp) {
			t.Errorf("ordering violation at [%d..%d]: %v > %v (want DESC)",
				i-1, i, cur.Timestamp, prev.Timestamp)
		}
	}

	// --- Keyset pagination across a page boundary ---
	// With pageSize=2 we need ceil(5/2)=3 pages to collect all 5 rows.
	const pageSize = 2
	var collected []string
	var cursorVals []any
	const maxIter = 10
	for iter := 0; iter < maxIter; iter++ {
		page, pErr := store.QueryCrossTenant(ctx, ctv, ledger.AuditFilters{},
			query.ListParams{
				Limit:        pageSize,
				Sort:         ledger.QuerySort(),
				CursorValues: cursorVals,
			})
		require.NoError(t, pErr, "QueryCrossTenant page %d", iter)
		hasMore := len(page) > pageSize
		visible := page
		if hasMore {
			visible = page[:pageSize]
		}
		for _, e := range visible {
			collected = append(collected, e.EventID)
		}
		if !hasMore {
			break
		}
		last := visible[len(visible)-1]
		cursorVals = []any{last.Timestamp.Format(time.RFC3339Nano), last.ID}
	}
	assert.Len(t, collected, 5, "keyset pagination must visit all 5 rows exactly once")
	// No duplicates.
	seen := make(map[string]bool, len(collected))
	for _, id := range collected {
		if seen[id] {
			t.Errorf("keyset pagination duplicate EventID: %q", id)
		}
		seen[id] = true
	}
}

// TestAuditCrossTenant_ServingRole_CannotReadCrossTenant is the KEY security
// assertion for migration 065. It proves that the restricted serving role
// gocell_rls_app (NOSUPERUSER NOBYPASSRLS — see restrictedAppPool) cannot
// read another tenant's audit rows even after migration 065 adds the permissive
// audit_admin_read_all policy for gocell_audit_admin.
//
// The permissive policy is scoped TO gocell_audit_admin. PG evaluates
// per-role policies: gocell_rls_app only sees the tenant_isolation policy
// (USING(tenant_id = NULLIF(current_setting('app.tenant_id',true), ”))),
// which restricts reads to the GUC-scoped tenant. Cross-tenant rows are never
// visible to the serving role — migration 065 must NOT widen gocell_app.
//
// Mirrors rls_force_integration_test.go's approach: SET LOCAL via
// TxManager.RunInTx + tenant.WithScope (GUC injection), then count rows
// whose tenant_id does NOT match the current scope. That count must be 0.
func TestAuditCrossTenant_ServingRole_CannotReadCrossTenant(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	ownerPool := openPerTestPool(t, dsn)

	// Provision gocell_audit_admin + apply migration 065 so the permissive
	// policy exists — this is the scenario we want to test.
	_ = provisionAuditAdminPool(t, dsn, ownerPool)

	// Open a restricted serving-role pool (NOSUPERUSER NOBYPASSRLS).
	appPool := restrictedAppPool(t, dsn, ownerPool)
	tm := NewTxManager(appPool)

	// Seed rows via the owner pool (bypasses RLS, full INSERT rights).
	// rlsTenantA row: proves the serving role CAN read its own-tenant rows.
	// ctTenantA / ctTenantB rows: used for the cross-tenant leak assertion.
	base := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	insertAuditRowAsOwner(t, ownerPool,
		"ct-sec-own1", ctNSAuditcore, "ev-sec-own1", "actor-own",
		string(rlsTenantA), 1, base.Add(-time.Second))
	insertAuditRowAsOwner(t, ownerPool,
		"ct-sec-a1", ctNSAuditcore, "ev-sec-a1", "actor-a", ctTenantA, 1, base)
	insertAuditRowAsOwner(t, ownerPool,
		"ct-sec-b1", ctNSAuditcore, "ev-sec-b1", "actor-b", ctTenantB, 1, base.Add(time.Second))

	// The serving role must be able to read its OWN tenant's rows via GUC scope.
	// rlsTenantA now has 1 seeded row — ownCount must be > 0.
	ownCount := scopedCountAudit(t, tm, rlsTenantA, ctNSAuditcore)
	assert.Greater(t, ownCount, 0,
		"[SEC] gocell_rls_app must be able to read its own-tenant rows (ownCount must be > 0): "+
			"the RLS policy must NOT block the serving role from reading its own tenant's audit rows")

	// The serving role must NOT see other tenants' rows even with migration 065
	// in place. We simulate the gocell_app serving role by reading inside a
	// scoped transaction where GUC = ctTenantA and counting how many rows have
	// tenant_id = ctTenantB (i.e. a different tenant's rows that leaked).
	var crossTenantCount int
	err := tm.RunInTx(tenant.WithScope(context.Background(), tenant.TenantID(ctTenantA)), func(ctx context.Context) error {
		tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
		require.True(t, ok, "ambient tx must be present")
		// The query deliberately omits a tenant_id predicate — RLS is the only
		// filter. If migration 065 leaked the permissive policy to gocell_app,
		// this query would return ctTenantB rows.
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_entries WHERE tenant_id = $1`, ctTenantB,
		).Scan(&crossTenantCount)
	})
	require.NoError(t, err, "scoped SELECT must succeed for serving role")
	assert.Equal(t, 0, crossTenantCount,
		"[SEC] gocell_rls_app must NOT see other tenant's audit rows after migration 065: "+
			"the audit_admin_read_all policy is scoped TO gocell_audit_admin only and must not widen the serving role")

	// Double-check: a predicate-free SELECT must also be tenant-scoped.
	// Scoped to ctTenantA → must NOT return ctTenantB's row.
	var totalCount int
	err = tm.RunInTx(tenant.WithScope(context.Background(), tenant.TenantID(ctTenantA)), func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_entries WHERE namespace = $1`, ctNSAuditcore,
		).Scan(&totalCount)
	})
	require.NoError(t, err)
	// ctTenantA has 1 row in ctNSAuditcore; ctTenantB's row must not be visible.
	assert.Equal(t, 1, totalCount,
		"serving role scoped to ctTenantA must see exactly 1 row in auditcore (its own), not ctTenantB's row")
}

// TestAuditAdminReadyCheck_PG exercises the enhanced composition-time / readyz
// preflight (#1810 F3) against a live admin pool. The positive path proves the
// strengthened SQL actually runs end-to-end — including that the restricted
// gocell_audit_admin role can itself read pg_policies to verify the policy shape —
// and the negative path proves the new policy-shape assertion fails-closed when the
// audit_admin_read_all policy is absent (the residual the prior SELECT-only check
// missed). The wrong-role identity dimension is covered by the pure-verdict unit
// test TestAuditAdminRoleResult.
//
// Run: go test -tags=integration ./adapters/postgres/ -run 'AuditAdminReadyCheck'
func TestAuditAdminReadyCheck_PG(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	ownerPool := openPerTestPool(t, dsn)
	adminPool := provisionAuditAdminPool(t, dsn, ownerPool)
	ctx := context.Background()

	// Positive: a correctly-provisioned gocell_audit_admin pool passes the full
	// preflight (role identity + attributes + SELECT + policy shape).
	require.NoError(t, adminPool.AuditAdminReadyCheck(ctx),
		"AuditAdminReadyCheck must pass for a correctly-provisioned gocell_audit_admin pool")

	// Negative: drop the role-scoped policy (via the owner) → the policy-shape
	// check must fail-closed, even though role identity + SELECT still hold.
	_, dropErr := ownerPool.DB().Exec(ctx, `DROP POLICY IF EXISTS audit_admin_read_all ON audit_entries`)
	require.NoError(t, dropErr, "drop audit_admin_read_all policy")

	err := adminPool.AuditAdminReadyCheck(ctx)
	require.Error(t, err, "AuditAdminReadyCheck must fail when audit_admin_read_all policy is absent")
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded), "expected *errcode.Error, got %T: %v", err, err)
	assert.Equal(t, ErrAdapterPGSchemaShape, coded.Code)
}

// TestAuditCrossTenantStore_PG_Conformance wires the PG-backed
// AuditCrossTenantStore into storetest.RunCrossTenantQueryConformance. The
// factory:
//  1. Opens a per-test DB clone (sharedPG.CloneDSN).
//  2. Provisions gocell_audit_admin and applies migration 065.
//  3. Seeds the supplied []*ledger.Entry via direct INSERT as owner (no protocol
//     HMAC — conformance asserts query-plane behavior, not chain correctness).
//  4. Returns an *AuditCrossTenantStore backed by the admin-role pool.
//
// Run: go test -tags=integration ./adapters/postgres/ -run 'CrossTenant'
func TestAuditCrossTenantStore_PG_Conformance(t *testing.T) {
	factory := buildCrossTenantPGFactory(t)
	storetest.RunCrossTenantQueryConformance(t, factory)
}

// buildCrossTenantPGFactory returns a storetest.CrossTenantFactory that:
//   - clones a fresh per-test DB
//   - provisions gocell_audit_admin + migration 065
//   - seeds the supplied entries via direct INSERT as superuser owner
//   - returns an *AuditCrossTenantStore + cleanup
//
// Each conformance sub-test receives its own DB clone so concurrent subtests
// do not share state (isolation matches the existing storetest PG factory pattern).
func buildCrossTenantPGFactory(t *testing.T) storetest.CrossTenantFactory {
	t.Helper()
	return func(subT *testing.T, seed []*ledger.Entry) (ledger.CrossTenantQueryStore, func()) {
		subT.Helper()

		// Fresh per-test DB clone — fully migrated (sharedPG template has all migrations).
		dsn := sharedPG.CloneDSN(subT)
		ownerPool := openPerTestPool(subT, dsn)

		// Provision gocell_audit_admin + apply migration 065 (idempotent).
		adminPool := provisionAuditAdminPool(subT, dsn, ownerPool)

		// Seed entries via direct INSERT as superuser — one chain per (namespace, tenantID)
		// with monotonically increasing seq_no per chain key. seq_no must be unique per
		// (namespace, tenant_id) chain or the UNIQUE constraint fires.
		if len(seed) > 0 {
			seedCrossTenantEntries(subT, ownerPool, seed)
		}

		// Build the store backed by the admin-role pool.
		store, storeErr := NewAuditCrossTenantStore(adminPool.DB())
		if storeErr != nil {
			subT.Fatalf("NewAuditCrossTenantStore: %v", storeErr)
		}

		cleanup := func() {
			// Per-test pools are closed by t.Cleanup registered in provisionAuditAdminPool
			// and openPerTestPool — no explicit action needed here.
		}
		return store, cleanup
	}
}

// seedCrossTenantEntries inserts each entry from seed into audit_entries via
// the superuser pool. Chain seq_no is assigned per (namespace, tenantID) so the
// UNIQUE(namespace, tenant_id, seq_no) constraint is satisfied. The namespace
// column is derived from the entry's EventID prefix (entries with EventID
// starting "ct-" use "auditcore"; others use "bootstrap") to match the
// storetest.ctSeed fixture which does not carry a namespace field but uses
// event IDs like "ct-a1", "ct-b1".
//
// The timing column uses entry.Timestamp (from ctSeed) so pagination ordering
// assertions have deterministic timestamps to compare against.
func seedCrossTenantEntries(t *testing.T, ownerPool *Pool, entries []*ledger.Entry) {
	t.Helper()
	ctx := context.Background()

	// Track seq_no per (namespace, tenantID) chain.
	type chainKey struct{ ns, tenantID string }
	seqNos := make(map[chainKey]int64)

	for _, e := range entries {
		// Derive namespace: conformance entries all use the "auditcore" namespace
		// (storetest.ctSeed does not distinguish namespaces at the entry level;
		// the MemStore representation is namespace-agnostic). We use "auditcore"
		// for all conformance seed entries so the predicate-free query returns them.
		ns := "auditcore"

		tid := e.TenantID // may be "" for system rows

		key := chainKey{ns: ns, tenantID: tid}
		seqNos[key]++
		seqNo := seqNos[key]

		prevHash := ""
		if seqNo > 1 {
			prevHash = auditHash64
		}

		// Use entry.Timestamp if non-zero; fall back to a deterministic offset.
		ts := e.Timestamp
		if ts.IsZero() {
			ts = clock.Real().Now()
		}

		// Generate a deterministic row ID (event_id is a UNIQUE key per chain). The
		// id column is uuid, so derive a stable valid UUID from eventID+suffix.
		// Unlike insertAuditRowSQL (which hardcodes empty subject_id/trace_id), this
		// persists the entry's SubjectID + TraceID so the cross-tenant SubjectID/
		// TraceID filter conformance sub-tests exercise real predicate matching.
		_, err := ownerPool.DB().Exec(ctx, insertCrossTenantRowSQL,
			auditRowUUID(e.EventID+"-pg"), // $1 id (uuid) — unique per chain entry
			ns,                            // $2 namespace
			seqNo,                         // $3 seq_no
			e.EventID,                     // $4 event_id (conformance unique key)
			e.EventType,                   // $5 event_type
			e.ActorID,                     // $6 actor_id
			e.SubjectID,                   // $7 subject_id
			tid,                           // $8 tenant_id
			e.TraceID,                     // $9 trace_id
			ts,                            // $10 occurred_at + timestamp
			prevHash,                      // $11 prev_hash
			auditHash64,                   // $12 hash
		)
		require.NoError(t, err, "seedCrossTenantEntries: insert %s (ns=%s tenant=%s seq=%d)",
			e.EventID, ns, tid, seqNo)
	}
}
