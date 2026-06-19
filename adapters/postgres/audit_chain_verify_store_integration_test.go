//go:build integration

package postgres

// #1755 AuditChainVerifyStore live-PG integration tests.
//
// These exercise the real gocell_audit_admin admin read pool (permissive
// audit_admin_read_all RLS policy, migration 065) enumerating + full-chain
// verifying per-(namespace, tenant) audit chains seeded with GENUINE HMAC values
// via the production LedgerStore.Append path (NOT the dummy-hash insert helper the
// #1810 query-plane tests use — chain integrity is exactly what these tests assert).
//
// Not parallel: gocell_audit_admin role provisioning is cluster-global (see the
// #1810 file's note); these reuse provisionAuditAdminPool / openPerTestPool /
// restrictedAppPool from the sibling integration test files.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// newVerifyProtocol builds a real HMAC Protocol for namespace ns with a key
// derived from seed (distinct seeds → distinct keys, mirroring the independent
// relay/bootstrap chain keys in production).
func newVerifyProtocol(t *testing.T, ns string, seed byte) *ledger.Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i+1) ^ seed
	}
	nsid, err := ledger.ParseNamespaceID(ns)
	require.NoError(t, err, "ParseNamespaceID(%q)", ns)
	p, err := ledger.NewProtocol(nsid, key,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}))
	require.NoError(t, err, "NewProtocol(%q)", ns)
	return p
}

// appendRealChain appends n genuine-HMAC entries for (protocol namespace, tenantID)
// via the owner-pool LedgerStore. Tenant rows are appended under tenant.WithScope so
// the FORCE-RLS WITH CHECK is satisfied; system rows (tenantID="") are appended
// unscoped (the OR tenant_id=” branch). Returns nothing — failures fail the test.
func appendRealChain(t *testing.T, store *LedgerStore, tenantID string, n int) {
	t.Helper()
	base := time.Date(2025, 6, 3, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		e := &ledger.Entry{
			EventID:    fmt.Sprintf("%s-%s-%d", store.namespace(), tenantID, i),
			EventType:  "verify.it",
			ActorID:    "actor",
			TenantID:   tenantID,
			OccurredAt: base.Add(time.Duration(i) * time.Second),
			Payload:    []byte(`{}`),
		}
		ctx := context.Background()
		if tenantID != "" {
			ctx = tenant.WithScope(ctx, tenant.TenantID(tenantID))
		}
		require.NoError(t, store.Append(ctx, e), "Append ns=%s tenant=%s i=%d", store.namespace(), tenantID, i)
	}
}

// buildVerifyFixture clones a DB, provisions the admin pool, seeds genuine chains
// (relay tenant-A=3, relay tenant-B=2, relay system=1, bootstrap system=2) and
// returns the admin-pool verify store plus the owner pool (for tampering).
func buildVerifyFixture(t *testing.T) (store *AuditChainVerifyStore, ownerPool *Pool) {
	t.Helper()
	dsn := sharedPG.CloneDSN(t)
	ownerPool = openPerTestPool(t, dsn)
	adminPool := provisionAuditAdminPool(t, dsn, ownerPool)

	relayProto := newVerifyProtocol(t, ctNSAuditcore, 0x00)
	bootstrapProto := newVerifyProtocol(t, ctNSBootstrap, 0xFF)

	tm := NewTxManager(ownerPool)
	relayStore, err := NewLedgerStore(ownerPool.DB(), tm, relayProto, clock.Real())
	require.NoError(t, err, "NewLedgerStore relay")
	bootstrapStore, err := NewLedgerStore(ownerPool.DB(), tm, bootstrapProto, clock.Real())
	require.NoError(t, err, "NewLedgerStore bootstrap")

	appendRealChain(t, relayStore, ctTenantA, 3)
	appendRealChain(t, relayStore, ctTenantB, 2)
	appendRealChain(t, relayStore, "", 1)
	appendRealChain(t, bootstrapStore, "", 2)

	store, err = NewAuditChainVerifyStore(context.Background(), adminPool, relayProto, bootstrapProto)
	require.NoError(t, err, "NewAuditChainVerifyStore")
	return store, ownerPool
}

// TestAuditChainVerifyStore_PG_EnumerateAndVerify proves the admin pool enumerates
// every (namespace, tenant) chain across tenants + namespaces and full-chain
// verifies each genuine HMAC chain as valid.
func TestAuditChainVerifyStore_PG_EnumerateAndVerify(t *testing.T) {
	store, _ := buildVerifyFixture(t)
	ctx := context.Background()

	refs, err := store.EnumerateChains(ctx)
	require.NoError(t, err, "EnumerateChains")

	type key struct{ ns, tenant string }
	got := map[key]ledger.ChainRef{}
	for _, r := range refs {
		got[key{r.Namespace, r.TenantID}] = r
	}
	want := map[key]int64{
		{ctNSAuditcore, ctTenantA}: 3,
		{ctNSAuditcore, ctTenantB}: 2,
		{ctNSAuditcore, ""}:        1,
		{ctNSBootstrap, ""}:        2,
	}
	require.Len(t, got, len(want), "enumerated chains: %+v", got)
	for k, maxSeq := range want {
		r, ok := got[k]
		require.True(t, ok, "missing chain %s/%s", k.ns, k.tenant)
		assert.Equal(t, maxSeq, r.MaxSeq, "chain %s/%s MaxSeq", k.ns, k.tenant)
		assert.Equal(t, int64(1), r.MinSeq, "chain %s/%s MinSeq (genesis present)", k.ns, k.tenant)

		valid, firstInvalid, vErr := store.VerifyChain(ctx, r.Namespace, r.TenantID, 1, r.MaxSeq)
		require.NoError(t, vErr, "VerifyChain %s/%s", k.ns, k.tenant)
		assert.True(t, valid, "chain %s/%s must verify valid", k.ns, k.tenant)
		assert.Equal(t, int64(-1), firstInvalid, "valid chain firstInvalidSeq")
	}
}

// TestAuditChainVerifyStore_PG_Tampered proves a corrupted row (owner UPDATE of the
// hash) makes VerifyChain report valid=false at the corrupted seq.
func TestAuditChainVerifyStore_PG_Tampered(t *testing.T) {
	store, ownerPool := buildVerifyFixture(t)
	ctx := context.Background()

	_, err := ownerPool.DB().Exec(ctx,
		`UPDATE audit_entries SET hash = $1 WHERE namespace = $2 AND tenant_id = $3 AND seq_no = $4`,
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", ctNSAuditcore, ctTenantA, 2)
	require.NoError(t, err, "tamper UPDATE")

	valid, firstInvalid, vErr := store.VerifyChain(ctx, ctNSAuditcore, ctTenantA, 1, 3)
	require.NoError(t, vErr, "VerifyChain (tamper is not an infra error)")
	assert.False(t, valid, "tampered chain must verify invalid")
	assert.Equal(t, int64(2), firstInvalid, "firstInvalidSeq must be the tampered seq")
}

// TestAuditChainVerifyStore_PG_UnknownNamespace proves a chain whose namespace has
// no registered protocol fails CLOSED (error), never a false valid=false.
func TestAuditChainVerifyStore_PG_UnknownNamespace(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	ownerPool := openPerTestPool(t, dsn)
	adminPool := provisionAuditAdminPool(t, dsn, ownerPool)
	relayProto := newVerifyProtocol(t, ctNSAuditcore, 0x00)

	// Store registers ONLY the relay protocol — the bootstrap namespace is unknown.
	store, err := NewAuditChainVerifyStore(context.Background(), adminPool, relayProto)
	require.NoError(t, err, "NewAuditChainVerifyStore")

	valid, _, vErr := store.VerifyChain(context.Background(), ctNSBootstrap, "", 1, 1)
	require.Error(t, vErr, "unknown namespace must fail closed")
	assert.False(t, valid, "unknown namespace must not report valid")
}

// TestAuditChainVerifyStore_PG_AbsentTenant proves that verifying a canonical
// tenant UUID that was never seeded returns a non-nil error and valid=false (the
// orchestrator only calls VerifyChain with MaxSeq>=1, so a missing tenant with a
// range of [1,1] finds no rows — not-found error, fail-closed).
func TestAuditChainVerifyStore_PG_AbsentTenant(t *testing.T) {
	store, _ := buildVerifyFixture(t)
	ctx := context.Background()

	// Use a canonical UUID that was never seeded.
	unseeded := "00000000-0000-0000-0000-000000000099"
	valid, _, err := store.VerifyChain(ctx, ctNSAuditcore, unseeded, 1, 1)
	if err == nil {
		t.Fatal("VerifyChain: expected non-nil error for absent tenant, got nil")
	}
	if valid {
		t.Error("VerifyChain: absent tenant must not report valid=true")
	}
}

// TestAuditChainVerifyStore_PG_ConstructorRejectsServingPool is the Medium
// fail-closed guard: the NOBYPASSRLS serving role (which would silently
// RLS-under-enumerate) cannot yield a usable verify store — the constructor's
// AuditAdminReadyCheck preflight rejects it.
func TestAuditChainVerifyStore_PG_ConstructorRejectsServingPool(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	ownerPool := openPerTestPool(t, dsn)
	servingPool := restrictedAppPool(t, dsn, ownerPool)
	relayProto := newVerifyProtocol(t, ctNSAuditcore, 0x00)

	_, err := NewAuditChainVerifyStore(context.Background(), servingPool, relayProto)
	require.Error(t, err, "serving pool must be rejected at construction")
	var coded *errcode.Error
	assert.True(t, errors.As(err, &coded), "expected *errcode.Error, got %T", err)
}
