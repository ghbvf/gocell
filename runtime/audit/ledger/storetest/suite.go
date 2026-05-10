// Package storetest provides a reusable Protocol-driven contract test suite for
// ledger.Store implementations. Each backend (mem, postgres) supplies a Factory
// and runs Run with the same Protocol; the suite derives test cases from the
// Protocol configuration so every backend is proved to honor the same protocol
// decisions.
//
// Helpers (NewTestProtocol / NewEntryFixture) are exported so future PG store
// integration tests reuse the same fixture surface; the path
// runtime/audit/ledger/storetest/ is in the
// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 archtest allowlist so calls to
// ledger.NewProtocol from this package are permitted.
//
// All test backends share NewTestProtocol so they prove parity on the same
// protocol decisions; backends differ only in their Factory implementation.
//
// MemStore restart simulation note: MemStore is ephemeral (no cross-factory
// persistence). The Restart_Recovery case documents the Tail-consistency
// invariant by replaying entries from storeA into storeB via GetBySeq/Append.
// A PG-backed store would restore state from DB on construction; the suite
// exercises the same Tail contract regardless of backend.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Factory constructs a fresh Store with a deterministic clock. Backends with
// per-test setup (e.g. PG schema reset) do it inside Factory; cleanup is the
// returned func and must be safe to call exactly once.
//
// The fakeClock return type is the concrete *clockmock.FakeClock rather than
// the clock.Clock interface — suite cases call fc.Advance() and fc.Now()
// directly, methods that only the concrete type carries.
type Factory func(t *testing.T) (store ledger.Store, fakeClock *clockmock.FakeClock, cleanup func())

// epochAnchor is the deterministic start time used by NewTestProtocol-driven
// fixtures. Anchored at 2025-01-01 UTC (round, far from epoch boundaries).
var epochAnchor = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// EpochAnchor returns the deterministic clock anchor used by storetest cases;
// backends constructing FakeClock from outside the suite (per-test setup hooks)
// should use this exact value so case timestamps line up.
func EpochAnchor() time.Time { return epochAnchor }

// NewTestProtocol constructs the canonical ledger protocol shape:
// RestartRecoveryStrictTailVerify + IdempotencyContentFingerprint + auditcore namespace.
// This call routes through ledger.NewProtocol; the archtest
// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 allowlist must include
// runtime/audit/ledger/storetest/ for this to compile-link cleanly.
func NewTestProtocol(t *testing.T) *ledger.Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ns, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		t.Fatalf("storetest: ParseNamespaceID: %v", err)
	}
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		t.Fatalf("storetest: NewTestProtocol failed: %v", err)
	}
	return p
}

// NewEntryFixture constructs an Entry with deterministic fields. eventID must be
// non-empty; other fields are set to reasonable defaults.
func NewEntryFixture(t *testing.T, eventID, eventType, actorID string, now time.Time) *ledger.Entry {
	t.Helper()
	if eventID == "" {
		t.Fatal("storetest: NewEntryFixture requires non-empty eventID")
	}
	if eventType == "" {
		eventType = "test.event"
	}
	if actorID == "" {
		actorID = "actor-test"
	}
	return &ledger.Entry{
		EventID:   eventID,
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: now,
		Payload:   []byte(`{}`),
	}
}

// Run executes the Protocol-driven contract suite against factory. All backends
// share NewTestProtocol to prove parity on the same protocol decisions.
//
// The protocol parameter is used by the Verify_Tampered* cases to recompute
// expected hashes for comparison. It must match the protocol passed to Factory.
func Run(t *testing.T, factory Factory, protocol *ledger.Protocol) {
	t.Helper()
	if factory == nil {
		t.Fatal("storetest.Run: factory must not be nil")
	}
	if protocol == nil {
		t.Fatal("storetest.Run: protocol must not be nil")
	}

	t.Run("Append_Tail_Round_Trip", func(t *testing.T) { runAppendTailRoundTrip(t, factory) })
	t.Run("Tail_EmptyStore", func(t *testing.T) { runTailEmptyStore(t, factory) })
	t.Run("Restart_Recovery", func(t *testing.T) { runRestartRecovery(t, factory) })
	t.Run("Idempotency_DuplicateContent", func(t *testing.T) { runIdempotencyDuplicateContent(t, factory) })
	t.Run("Concurrent_Append_HashChainValid", func(t *testing.T) { runConcurrentAppendHashChainValid(t, factory) })
	t.Run("StrictPayload_InvalidJSON", func(t *testing.T) { runStrictPayloadInvalidJSON(t, factory) })
	t.Run("Verify_FullRange", func(t *testing.T) { runVerifyFullRange(t, factory) })
	t.Run("Verify_TamperedHash", func(t *testing.T) { runVerifyTamperedHash(t, factory, protocol) })
	t.Run("Verify_TamperedPrevHash", func(t *testing.T) { runVerifyTamperedPrevHash(t, factory, protocol) })
	t.Run("GetBySeq_NotFound", func(t *testing.T) { runGetBySeqNotFound(t, factory) })
	t.Run("Query_ByFilters", func(t *testing.T) { runQueryByFilters(t, factory) })
}

// runAppendTailRoundTrip: Append persists entry; Tail advances; GetBySeq returns entry.
func runAppendTailRoundTrip(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e := NewEntryFixture(t, "evt-round-trip", "audit.test", "actor-1", fc.Now())
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	tail, err := store.Tail(context.Background())
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if tail.SeqNo != 1 {
		t.Errorf("Tail.SeqNo: got %d, want 1", tail.SeqNo)
	}
	if tail.EntryCount != 1 {
		t.Errorf("Tail.EntryCount: got %d, want 1", tail.EntryCount)
	}

	got, err := store.GetBySeq(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetBySeq(1): %v", err)
	}
	if got.EventID != e.EventID {
		t.Errorf("EventID: got %q, want %q", got.EventID, e.EventID)
	}
	if got.PrevHash != "" {
		t.Errorf("first entry PrevHash: got %q, want empty", got.PrevHash)
	}
	if got.Hash == "" {
		t.Error("first entry Hash must not be empty")
	}
}

// runTailEmptyStore: empty store returns zero TailSnapshot.
func runTailEmptyStore(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	tail, err := store.Tail(context.Background())
	if err != nil {
		t.Fatalf("Tail on empty store: %v", err)
	}
	if tail.SeqNo != 0 {
		t.Errorf("empty Tail.SeqNo: got %d, want 0", tail.SeqNo)
	}
	if tail.PrevHash != "" {
		t.Errorf("empty Tail.PrevHash: got %q, want empty", tail.PrevHash)
	}
	if tail.EntryCount != 0 {
		t.Errorf("empty Tail.EntryCount: got %d, want 0", tail.EntryCount)
	}
}

// runRestartRecovery: simulates restart by draining entries from storeA
// into storeB; Tail must match.
func runRestartRecovery(t *testing.T, factory Factory) {
	storeA, fc, cleanupA := factory(t)
	defer cleanupA()

	const n = 3
	for i := 1; i <= n; i++ {
		e := NewEntryFixture(t, fmt.Sprintf("restart-evt-%d", i), "restart.test", "actor", fc.Now())
		if err := storeA.Append(context.Background(), e); err != nil {
			t.Fatalf("storeA Append %d: %v", i, err)
		}
	}
	tailA, err := storeA.Tail(context.Background())
	if err != nil {
		t.Fatalf("storeA Tail: %v", err)
	}

	// Replay into storeB using a second factory call.
	storeB, _, cleanupB := factory(t)
	defer cleanupB()

	for seq := int64(1); seq <= int64(n); seq++ {
		src, err := storeA.GetBySeq(context.Background(), seq)
		if err != nil {
			t.Fatalf("storeA GetBySeq(%d): %v", seq, err)
		}
		replay := &ledger.Entry{
			EventID:   src.EventID,
			EventType: src.EventType,
			ActorID:   src.ActorID,
			Timestamp: src.Timestamp,
			Payload:   src.Payload,
		}
		if err := storeB.Append(context.Background(), replay); err != nil {
			t.Fatalf("storeB Append seq %d: %v", seq, err)
		}
	}
	tailB, err := storeB.Tail(context.Background())
	if err != nil {
		t.Fatalf("storeB Tail: %v", err)
	}
	if tailA.SeqNo != tailB.SeqNo {
		t.Errorf("restart SeqNo mismatch: A=%d B=%d", tailA.SeqNo, tailB.SeqNo)
	}
	if tailA.EntryCount != tailB.EntryCount {
		t.Errorf("restart EntryCount mismatch: A=%d B=%d", tailA.EntryCount, tailB.EntryCount)
	}
}

// runIdempotencyDuplicateContent: duplicate content fingerprint returns ErrAuditLedgerAlreadyExists.
func runIdempotencyDuplicateContent(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e1 := &ledger.Entry{
		EventID:   "idem-evt",
		EventType: "idempotency.test",
		ActorID:   "actor",
		Timestamp: fc.Now(),
		Payload:   []byte(`{"key":"value"}`),
	}
	if err := store.Append(context.Background(), e1); err != nil {
		t.Fatalf("first Append: %v", err)
	}

	// Second append with same fingerprint.
	e2 := &ledger.Entry{
		EventID:   e1.EventID,
		EventType: e1.EventType,
		ActorID:   e1.ActorID,
		Timestamp: e1.Timestamp,
		Payload:   e1.Payload,
	}
	err := store.Append(context.Background(), e2)
	if err == nil {
		t.Fatal("expected ErrAuditLedgerAlreadyExists for duplicate content")
	}
	assertErrCode(t, err, errcode.ErrAuditLedgerAlreadyExists)
}

// runConcurrentAppendHashChainValid: 100 concurrent appends; chain must be valid.
// F24: increased from 50 to 100 to align with PG integration test concurrency level.
func runConcurrentAppendHashChainValid(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	const n = 100
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			e := &ledger.Entry{
				EventID:   fmt.Sprintf("concurrent-%d", i),
				EventType: "concurrent.test",
				ActorID:   "actor",
				Timestamp: fc.Now(),
				Payload:   []byte(`{}`),
			}
			if appErr := store.Append(context.Background(), e); appErr != nil {
				errCh <- appErr
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for e := range errCh {
		t.Errorf("concurrent Append error: %v", e)
	}

	tail, err := store.Tail(context.Background())
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if tail.EntryCount != n {
		t.Errorf("EntryCount: got %d, want %d", tail.EntryCount, n)
	}

	valid, firstInvalid, err := store.Verify(context.Background(), 1, tail.SeqNo)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !valid {
		t.Errorf("hash chain invalid starting at seq %d", firstInvalid)
	}
}

// runStrictPayloadInvalidJSON: invalid JSON payload is rejected.
func runStrictPayloadInvalidJSON(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e := &ledger.Entry{
		EventID:   "strict-evt",
		EventType: "test",
		ActorID:   "actor",
		Timestamp: fc.Now(),
		Payload:   []byte(`{invalid`),
	}
	err := store.Append(context.Background(), e)
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
}

// runVerifyFullRange: Verify returns valid=true for a freshly appended range.
func runVerifyFullRange(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	for i := 1; i <= 5; i++ {
		e := NewEntryFixture(t, fmt.Sprintf("vf-%d", i), "verify.test", "actor", fc.Now())
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	valid, firstInvalid, err := store.Verify(context.Background(), 1, 5)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !valid {
		t.Errorf("Verify: expected valid, first invalid at seq %d", firstInvalid)
	}
}

// runVerifyTamperedHash: a MemStore entry with a corrupted Hash field must
// cause Verify to return valid=false at that seq_no.
//
// F5: negative Verify case using protocol.ComputeHash for expected-hash reference.
// This function uses a type-switch to access MemStore internals — only MemStore
// (the test-only in-process backend) supports direct field tampering; PG store
// tampered cases require external SQL manipulation and belong in integration tests.
func runVerifyTamperedHash(t *testing.T, factory Factory, protocol *ledger.Protocol) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e := NewEntryFixture(t, "tamper-hash-evt", "tamper.test", "actor", fc.Now())
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	ms, ok := store.(*ledger.MemStore)
	if !ok {
		t.Skip("Verify_TamperedHash requires *ledger.MemStore; skipping for non-MemStore backends")
	}

	// Tamper the stored entry's Hash via MemStore test helper.
	ms.MustTamperEntryHash(1, "tampered-hash-value")

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

	// Confirm protocol can still recompute the correct hash from stored data.
	entry, err := store.GetBySeq(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetBySeq: %v", err)
	}
	correctHash := protocol.ComputeHash(entry.PrevHash, entry)
	if entry.Hash == correctHash {
		t.Error("tampered hash unexpectedly matches recomputed hash")
	}
}

// runVerifyTamperedPrevHash: a MemStore entry with a corrupted PrevHash field
// must cause Verify to return valid=false at that seq_no.
//
// F5: negative Verify case; mirrors runVerifyTamperedHash but targets PrevHash linkage.
func runVerifyTamperedPrevHash(t *testing.T, factory Factory, protocol *ledger.Protocol) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	// Append two entries so seq 2 has a meaningful PrevHash linkage.
	for i, id := range []string{"prev-hash-evt-1", "prev-hash-evt-2"} {
		e := NewEntryFixture(t, id, "tamper.test", "actor", fc.Now())
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append %d: %v", i+1, err)
		}
	}

	ms, ok := store.(*ledger.MemStore)
	if !ok {
		t.Skip("Verify_TamperedPrevHash requires *ledger.MemStore; skipping for non-MemStore backends")
	}

	// Tamper the second entry's PrevHash — breaks the chain link between seq 1 and seq 2.
	ms.MustTamperEntryPrevHash(2, "tampered-prev-hash")

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
	_ = protocol // used for documentation; hash recomputation is in runVerifyTamperedHash
}

// runGetBySeqNotFound: missing seqNo returns errcode error.
func runGetBySeqNotFound(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	_, err := store.GetBySeq(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error for missing seqNo")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
}

// runQueryByFilters: Query returns only entries matching the filter.
func runQueryByFilters(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	for i := 1; i <= 6; i++ {
		et := "type.X"
		if i > 3 {
			et = "type.Y"
		}
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("qf-%d", i),
			EventType: et,
			ActorID:   "actor",
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	results, err := store.Query(context.Background(), ledger.AuditFilters{EventType: "type.X"}, ledger.QueryListParams{Limit: 50})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query(type.X): got %d, want 3", len(results))
	}
}

// assertErrCode asserts err wraps an *errcode.Error with the given Code.
func assertErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error with code %s, got %T: %v", want, err, err)
	}
	if coded.Code != want {
		t.Errorf("errcode mismatch: got %s, want %s (msg=%q)", coded.Code, want, coded.Message)
	}
}
