// Package ledger — in-package (white-box) test for MemChainVerifyStore (#1755).
//
// White-box (package ledger, not ledger_test) so it can seed per-(namespace,
// tenant) chains directly and tamper a stored entry's Hash via m.chains — the
// same access pattern as mem_store_tamper_test.go.
package ledger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// newChainVerifyProtocol builds a Protocol for namespace ns with a key derived
// from seed so two namespaces get DISTINCT HMAC keys (mirrors production: relay
// and bootstrap chains have independent keys).
func newChainVerifyProtocol(t *testing.T, ns string, seed byte) *Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i+1) ^ seed
	}
	nsid, err := ParseNamespaceID(ns)
	if err != nil {
		t.Fatalf("ParseNamespaceID(%q): %v", ns, err)
	}
	p, err := NewProtocol(nsid, key,
		WithRestartRecovery(RestartRecoveryStrictTailVerify{}),
		WithIdempotency(IdempotencyContentFingerprint{}))
	if err != nil {
		t.Fatalf("NewProtocol(%q): %v", ns, err)
	}
	return p
}

// appendN appends n entries for tenantID into store (real HMAC chain).
func appendN(t *testing.T, store *MemStore, tenantID string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		e := &Entry{
			EventID:   tenantID + "-evt-" + string(rune('0'+i)),
			EventType: "verify.test",
			ActorID:   "actor",
			TenantID:  tenantID,
			Timestamp: time.Date(2025, 1, 1, 0, 0, 0, i, time.UTC),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append tenant=%q i=%d: %v", tenantID, i, err)
		}
	}
}

// setChainEntryHash corrupts the stored Hash of (tenantID, seq) in store —
// white-box tamper seam, distinct name from mem_store_tamper_test.go's helpers.
func setChainEntryHash(store *MemStore, tenantID string, seq int64, newHash string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.chains[tenantID].entries[seq-1].Hash = newHash
}

func newTwoNamespaceVerifyStore(t *testing.T) (relay, bootstrap *MemStore, cv *MemChainVerifyStore) {
	t.Helper()
	fc := clockmock.New(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	relay, err := NewMemStore(newChainVerifyProtocol(t, "auditcore", 0x00), fc)
	if err != nil {
		t.Fatalf("NewMemStore relay: %v", err)
	}
	bootstrap, err = NewMemStore(newChainVerifyProtocol(t, "bootstrap", 0xFF), fc)
	if err != nil {
		t.Fatalf("NewMemStore bootstrap: %v", err)
	}
	cv, err = NewMemChainVerifyStore(relay, bootstrap)
	if err != nil {
		t.Fatalf("NewMemChainVerifyStore: %v", err)
	}
	return relay, bootstrap, cv
}

func TestNewMemChainVerifyStore_Rejects(t *testing.T) {
	t.Parallel()
	if _, err := NewMemChainVerifyStore(); err == nil {
		t.Fatal("expected error for zero stores")
	}
	if _, err := NewMemChainVerifyStore(nil); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestMemChainVerifyStore_EnumerateChains(t *testing.T) {
	t.Parallel()
	relay, bootstrap, cv := newTwoNamespaceVerifyStore(t)
	appendN(t, relay, "tenant-a", 3)
	appendN(t, relay, "tenant-b", 2)
	appendN(t, relay, "", 1)     // system sub-chain on relay
	appendN(t, bootstrap, "", 4) // bootstrap system chain

	refs, err := cv.EnumerateChains(context.Background())
	if err != nil {
		t.Fatalf("EnumerateChains: %v", err)
	}
	type key struct{ ns, tenant string }
	got := map[key]int64{}
	for _, r := range refs {
		got[key{r.Namespace, r.TenantID}] = r.MaxSeq
		if r.MinSeq != 1 {
			t.Errorf("chain %s/%s MinSeq=%d, want 1", r.Namespace, r.TenantID, r.MinSeq)
		}
	}
	want := map[key]int64{
		{"auditcore", "tenant-a"}: 3,
		{"auditcore", "tenant-b"}: 2,
		{"auditcore", ""}:         1,
		{"bootstrap", ""}:         4,
	}
	if len(got) != len(want) {
		t.Fatalf("enumerated %d chains, want %d: %+v", len(got), len(want), got)
	}
	for k, maxSeq := range want {
		if got[k] != maxSeq {
			t.Errorf("chain %s/%s MaxSeq=%d, want %d", k.ns, k.tenant, got[k], maxSeq)
		}
	}
}

func TestMemChainVerifyStore_EnumerateChains_Empty(t *testing.T) {
	t.Parallel()
	_, _, cv := newTwoNamespaceVerifyStore(t)
	refs, err := cv.EnumerateChains(context.Background())
	if err != nil {
		t.Fatalf("EnumerateChains: %v", err)
	}
	if refs == nil {
		t.Fatal("EnumerateChains returned nil slice, want empty non-nil")
	}
	if len(refs) != 0 {
		t.Fatalf("EnumerateChains on empty stores returned %d chains", len(refs))
	}
}

func TestMemChainVerifyStore_VerifyChain_Valid(t *testing.T) {
	t.Parallel()
	relay, bootstrap, cv := newTwoNamespaceVerifyStore(t)
	appendN(t, relay, "tenant-a", 3)
	appendN(t, bootstrap, "", 2)

	valid, firstInvalid, err := cv.VerifyChain(context.Background(), "auditcore", "tenant-a", 1, 3)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !valid || firstInvalid != -1 {
		t.Fatalf("VerifyChain relay/tenant-a: valid=%v firstInvalid=%d, want true/-1", valid, firstInvalid)
	}
	valid, _, err = cv.VerifyChain(context.Background(), "bootstrap", "", 1, 2)
	if err != nil || !valid {
		t.Fatalf("VerifyChain bootstrap/system: valid=%v err=%v, want true/nil", valid, err)
	}
}

func TestMemChainVerifyStore_VerifyChain_Tampered(t *testing.T) {
	t.Parallel()
	relay, _, cv := newTwoNamespaceVerifyStore(t)
	appendN(t, relay, "tenant-a", 3)
	setChainEntryHash(relay, "tenant-a", 2, "deadbeef")

	valid, firstInvalid, err := cv.VerifyChain(context.Background(), "auditcore", "tenant-a", 1, 3)
	if err != nil {
		t.Fatalf("VerifyChain: unexpected error: %v", err)
	}
	if valid {
		t.Error("VerifyChain: expected valid=false after Hash tamper")
	}
	if firstInvalid != 2 {
		t.Errorf("VerifyChain firstInvalidSeq=%d, want 2", firstInvalid)
	}
}

func TestMemChainVerifyStore_VerifyChain_UnknownNamespace(t *testing.T) {
	t.Parallel()
	relay, _, cv := newTwoNamespaceVerifyStore(t)
	appendN(t, relay, "tenant-a", 1)

	valid, _, err := cv.VerifyChain(context.Background(), "nosuchns", "tenant-a", 1, 1)
	if err == nil {
		t.Fatal("VerifyChain unknown namespace: expected fail-closed error, got nil")
	}
	if valid {
		t.Error("VerifyChain unknown namespace: must not report valid")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
}

func TestMemChainVerifyStore_VerifyChain_AbsentTenant(t *testing.T) {
	t.Parallel()
	relay, _, cv := newTwoNamespaceVerifyStore(t)
	appendN(t, relay, "tenant-a", 1)

	// A registered namespace but a tenant with no rows: verifying [1,1] finds no
	// entry → not-found error (the orchestrator only calls with MaxSeq>=1).
	valid, _, err := cv.VerifyChain(context.Background(), "auditcore", "ghost-tenant", 1, 1)
	if err == nil {
		t.Fatal("VerifyChain absent tenant: expected not-found error")
	}
	if valid {
		t.Error("VerifyChain absent tenant: must not report valid")
	}
}
