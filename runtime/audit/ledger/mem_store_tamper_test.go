// Package ledger — in-package (white-box) test file.
//
// This file uses `package ledger` (not `package ledger_test`) so the test
// helpers tamperEntryHash / tamperEntryPrevHash can access *MemStore's
// unexported entries field. The sibling mem_store_test.go uses
// `package ledger_test` (black-box, exercises only the public surface).
//
// Test-only physical exclusion (A-05, ADR docs/architecture/202605171800-
// adr-kernel-mustctor-removal.md): defining the tamper helpers in this
// _test.go file removes them from the production binary (Go `go build` does
// not compile _test.go), making "any production caller of tamper" not just
// banned but unrepresentable.
//
// Panic discipline: bare panic(fmt.Sprintf(...)) is used below for
// out-of-range diagnostics because PANIC-REGISTERED-01 archtest skips
// _test.go files. If future maintainers need a tamper helper in
// production source (mem_store.go), the panic must be wrapped with
// panicregister.Approved(...) per PANIC-REGISTERED-01.

package ledger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// tamperEntryHash directly modifies the Hash field of the stored entry at the
// given seq (1-indexed). Test-only helper — physically isolated to _test.go so
// it is excluded from the production binary.
//
// Panics with a plain fmt.Sprintf message on out-of-range seq. Test files are
// excluded from PANIC-REGISTERED-01 enforcement (archtest skips _test.go), so
// plain panic is acceptable here.
func (m *MemStore) tamperEntryHash(seq int64, newHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := seq - 1
	if int(idx) < 0 || int(idx) >= len(m.entries) {
		panic(fmt.Sprintf("tamperEntryHash: seq %d out of range [1, %d]", seq, len(m.entries)))
	}
	m.entries[idx].Hash = newHash
}

// tamperEntryPrevHash directly modifies the PrevHash field of the stored entry
// at the given seq (1-indexed). Test-only helper — physically isolated to
// _test.go so it is excluded from the production binary.
func (m *MemStore) tamperEntryPrevHash(seq int64, newPrevHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := seq - 1
	if int(idx) < 0 || int(idx) >= len(m.entries) {
		panic(fmt.Sprintf("tamperEntryPrevHash: seq %d out of range [1, %d]", seq, len(m.entries)))
	}
	m.entries[idx].PrevHash = newPrevHash
}

// newTestProtocolForTamper constructs a test Protocol; mirrors newTestProtocol
// in mem_store_test.go but lives in the same-package test file to access
// package-internal fields after tampering.
func newTestProtocolForTamper(t *testing.T) *Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ns, err := ParseNamespaceID("auditcore")
	if err != nil {
		t.Fatalf("ParseNamespaceID: %v", err)
	}
	p, err := NewProtocol(
		WithChainHMAC(key),
		WithNamespace(ns),
		WithRestartRecovery(RestartRecoveryStrictTailVerify{}),
		WithIdempotency(IdempotencyContentFingerprint{}),
	)
	if err != nil {
		t.Fatalf("NewProtocol: %v", err)
	}
	return p
}

// TestMemStore_VerifyTamperedHash: a MemStore entry with a corrupted Hash field
// must cause Verify to return valid=false at that seq_no.
//
// F5: negative Verify case; moved here from storetest.runVerifyTamperedHash
// (A-05 refactor: MustTamperEntryHash removed from production export surface).
func TestMemStore_VerifyTamperedHash(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	p := newTestProtocolForTamper(t)

	store, err := NewMemStore(p, fc)
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}

	e := &Entry{
		EventID:   "tamper-hash-evt",
		EventType: "tamper.test",
		ActorID:   "actor",
		Timestamp: fc.Now(),
		Payload:   []byte(`{}`),
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Tamper the stored entry's Hash via the unexported test helper.
	store.tamperEntryHash(1, "tampered-hash-value")

	valid, firstInvalid, err := store.Verify(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
	if valid {
		t.Error("Verify: expected valid=false after Hash tampering")
	}
	if firstInvalid != 1 {
		t.Errorf("Verify: firstInvalidSeq: got %d, want 1", firstInvalid)
	}

	// Confirm the stored hash no longer matches the recomputed hash.
	entry, err := store.GetBySeq(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetBySeq: %v", err)
	}
	correctHash := p.ComputeHash(entry.PrevHash, entry)
	if entry.Hash == correctHash {
		t.Error("tampered hash unexpectedly matches recomputed hash")
	}
}

// TestMemStore_VerifyTamperedPrevHash: a MemStore entry with a corrupted
// PrevHash field must cause Verify to return valid=false at that seq_no.
//
// F5: negative Verify case; moved here from storetest.runVerifyTamperedPrevHash
// (A-05 refactor: MustTamperEntryPrevHash removed from production export surface).
func TestMemStore_VerifyTamperedPrevHash(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	p := newTestProtocolForTamper(t)

	store, err := NewMemStore(p, fc)
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}

	// Append two entries so seq 2 has a meaningful PrevHash linkage.
	for i, id := range []string{"prev-hash-evt-1", "prev-hash-evt-2"} {
		e := &Entry{
			EventID:   id,
			EventType: "tamper.test",
			ActorID:   "actor",
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append %d: %v", i+1, err)
		}
	}

	// Tamper the second entry's PrevHash — breaks the chain link between seq 1 and seq 2.
	store.tamperEntryPrevHash(2, "tampered-prev-hash")

	valid, firstInvalid, err := store.Verify(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
	if valid {
		t.Error("Verify: expected valid=false after PrevHash tampering")
	}
	if firstInvalid != 2 {
		t.Errorf("Verify: firstInvalidSeq: got %d, want 2", firstInvalid)
	}
}
