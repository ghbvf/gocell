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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest" // test funnel; storetest is testing-helper package, errcodetest import is intentional (not a test-only import in a non-_test.go file)
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

const fmtErrGetBySeq1 = "GetBySeq(1): %v"

// entryStoreAssignedFields enumerates Entry fields that Append/GetBySeq write
// or compute, NOT the caller's source-of-truth. AssertEntryRoundTrip skips
// these because they intentionally differ between the caller's literal and
// the store's persisted view.
var entryStoreAssignedFields = map[string]struct{}{
	"SeqNo":    {},
	"ID":       {},
	"PrevHash": {},
	"Hash":     {},
}

// AssertEntryRoundTrip verifies that all caller-supplied (non-store-assigned)
// fields of src round-trip byte-for-byte through Append + GetBySeq, comparing
// every exported field via reflect. When a new caller-supplied field is added
// to ledger.Entry (the canonical HMAC input expands), this helper picks it up
// automatically — no edit needed here.
//
// Store-assigned fields (SeqNo, ID, PrevHash, Hash) are skipped: Append writes
// them, so the persisted view legitimately differs from the source literal.
// Chain-level Hash parity is asserted separately by hash-parity helpers
// (Protocol_HashParity, PrincipalFields_RoundTrip).
//
// Drift between the Entry field set and AUDIT-HASH-INPUT-FROZEN-01 A1 is caught
// by that archtest first; this helper makes the runtime round-trip behavior
// visible against the same surface, so an add-field-but-drop-from-store
// regression fails here too.
func AssertEntryRoundTrip(t *testing.T, src, got *ledger.Entry) {
	t.Helper()
	if src == nil || got == nil {
		t.Fatalf("AssertEntryRoundTrip: nil entry (src=%v got=%v)", src, got)
	}
	rSrc := reflect.ValueOf(*src)
	rGot := reflect.ValueOf(*got)
	typ := rSrc.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, skip := entryStoreAssignedFields[f.Name]; skip {
			continue
		}
		srcVal := rSrc.Field(i).Interface()
		gotVal := rGot.Field(i).Interface()
		if !entryFieldEqual(srcVal, gotVal) {
			t.Errorf("AssertEntryRoundTrip: field %s mismatch: got %v, want %v",
				f.Name, gotVal, srcVal)
		}
	}
}

// entryFieldEqual compares two Entry field values using type-appropriate
// equality: time.Time via .Equal (ignores monotonic clock + UTC normalisation),
// []byte via bytes.Equal, scalar types via reflect.DeepEqual.
func entryFieldEqual(a, b any) bool {
	switch va := a.(type) {
	case time.Time:
		vb, ok := b.(time.Time)
		return ok && va.Equal(vb)
	case []byte:
		vb, ok := b.([]byte)
		return ok && bytes.Equal(va, vb)
	}
	return reflect.DeepEqual(a, b)
}

// redeliveryAdvance is the clock advance used in at-least-once redelivery
// simulation test cases (F-CR-2 idempotency regression guard).
const redeliveryAdvance = 5 * time.Second

// Fatalf format strings extracted per go:S1192 (used 3+ times each).
const (
	msgAppend    = "Append: %v"
	msgVerify    = "Verify: %v"
	msgAppendIdx = "Append %d: %v"
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

// TestHMACKey returns the deterministic 32-byte HMAC key NewTestProtocol uses.
// Exposed so cross-implementation parity tests (RunPrincipalFieldsRoundTrip)
// can recompute the canonical HMAC byte-for-byte via ReferenceComputeHash
// without going through Protocol.ComputeHash. The returned slice is a fresh
// copy each call so callers may zero or mutate it (e.g. before passing into
// WithChainHMAC, which itself wipes the input slice after a defensive copy).
//
// TEST ONLY. This is a fixed, public, low-entropy key (bytes 1..32) for
// deterministic test parity — it provides no secrecy and must never be used to
// seal a production audit chain.
func TestHMACKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// NewTestProtocol constructs the canonical ledger protocol shape:
// RestartRecoveryStrictTailVerify + IdempotencyContentFingerprint + auditcore namespace.
// This call routes through ledger.NewProtocol; the archtest
// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 allowlist must include
// runtime/audit/ledger/storetest/ for this to compile-link cleanly.
//
// Accepts testing.TB (not *testing.T) so fuzz targets can construct the
// protocol in their setup phase, where only a *testing.F is in scope.
func NewTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	key := TestHMACKey()
	ns, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		t.Fatalf("storetest: ParseNamespaceID: %v", err)
	}
	p, err := ledger.NewProtocol(
		ns,
		key,
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
//
// All 5 Principal/Correlation/OccurredAt fields are populated with non-zero
// fixture values so the default storetest suite exercises the full 12-field
// HMAC chain rather than the empty-string degenerate case. Callers that want
// to exercise the zero-value path explicitly (e.g. a PR-A1 transition test)
// should construct the Entry literal directly instead of going through this
// helper.
//
// When the Entry shape grows (e.g. PR-A2 wiring more producer-supplied
// metadata), update this fixture AND the referenceHashInput mirror struct
// above in the same change — drift between either side and
// runtime/audit/ledger.auditHashInput is caught by archtest
// AUDIT-HASH-INPUT-FROZEN-01, but only after the test fixture has been
// updated to populate the new fields.
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
		EventID:       eventID,
		EventType:     eventType,
		ActorID:       actorID,
		SubjectID:     "subject-test",
		TenantID:      "tenant-test",
		SessionID:     "session-test",
		CorrelationID: "corr-test",
		OccurredAt:    now.Add(principalOccurredAtSkew),
		Timestamp:     now,
		Payload:       []byte(`{}`),
	}
}

// Run executes the Protocol-driven contract suite against factory. All backends
// share NewTestProtocol to prove parity on the same protocol decisions.
//
// The protocol parameter is the same instance the Factory uses internally; the
// Protocol_HashParity subtest re-computes ComputeHash on appended entries to
// prove the store's persisted hash matches the protocol's output byte-for-byte
// (the core single-source contract between Store implementations and Protocol).
// The Verify_Tampered* cases previously accepted via this parameter have been
// relocated to mem_store_tamper_test.go (A-05 refactor).
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
	t.Run("Idempotency_DifferentTimestamp_SameEventID", func(t *testing.T) { runIdempotencyDifferentTimestampSameEventID(t, factory) })
	t.Run("Concurrent_Append_HashChainValid", func(t *testing.T) { runConcurrentAppendHashChainValid(t, factory) })
	t.Run("StrictPayload_InvalidJSON", func(t *testing.T) { runStrictPayloadInvalidJSON(t, factory) })
	t.Run("Verify_FullRange", func(t *testing.T) { runVerifyFullRange(t, factory) })
	t.Run("GetBySeq_NotFound", func(t *testing.T) {
		store, _, cleanup := factory(t)
		defer cleanup()
		_, err := store.GetBySeq(context.Background(), 9999)
		errcodetest.AssertCode(t, err, errcode.ErrAuditLedgerNotFound)
	})
	t.Run("Query_ByFilters", func(t *testing.T) { runQueryByFilters(t, factory) })
	t.Run("Append_MultiKey_Payload_RoundTrip", func(t *testing.T) { runAppendMultiKeyPayloadRoundTrip(t, factory) })
	t.Run("Query_Ordering_TimestampDesc_IDAsc", func(t *testing.T) { runQueryOrderingTimestampDescIDAsc(t, factory) })
	t.Run("Query_Keyset_Pagination", func(t *testing.T) { runQueryKeysetPagination(t, factory) })
	t.Run("Query_EmptySort_Rejected", func(t *testing.T) { runQueryEmptySortRejected(t, factory) })
	t.Run("Query_InvalidCursor_Rejected", func(t *testing.T) { runQueryInvalidCursorRejected(t, factory) })
	t.Run("Protocol_HashParity", func(t *testing.T) { runProtocolHashParity(t, factory, protocol) })
	t.Run("PrincipalFields_RoundTrip", func(t *testing.T) { RunPrincipalFieldsRoundTrip(t, factory, protocol) })
	t.Run("Query_ByTraceID", func(t *testing.T) { runQueryByTraceID(t, factory) })
	t.Run("TraceID_NotInHashChain", func(t *testing.T) { runTraceIDNotInHashChain(t, factory, protocol) })
}

// runAppendTailRoundTrip: Append persists entry; Tail advances; GetBySeq returns entry.
func runAppendTailRoundTrip(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e := NewEntryFixture(t, "evt-round-trip", "audit.test", "actor-1", fc.Now())
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf(msgAppend, err)
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
		t.Fatalf(fmtErrGetBySeq1, err)
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
// into storeB; Tail snapshot AND every Entry field round-trip via
// AssertEntryRoundTrip, plus the tail Hash must match byte-for-byte across
// stores (proves all 12 canonical-HMAC fields participated in chain replay,
// not just the originally-asserted SeqNo/EntryCount pair).
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

	// Replay into storeB using a second factory call. Replay copies the FULL
	// Entry shape (every caller-supplied field) so the cross-store chain truly
	// witnesses all 12 canonical-HMAC fields, not just the historical 5.
	storeB, _, cleanupB := factory(t)
	defer cleanupB()

	for seq := int64(1); seq <= int64(n); seq++ {
		src, err := storeA.GetBySeq(context.Background(), seq)
		if err != nil {
			t.Fatalf("storeA GetBySeq(%d): %v", seq, err)
		}
		replay := &ledger.Entry{
			EventID:       src.EventID,
			EventType:     src.EventType,
			ActorID:       src.ActorID,
			SubjectID:     src.SubjectID,
			TenantID:      src.TenantID,
			SessionID:     src.SessionID,
			CorrelationID: src.CorrelationID,
			TraceID:       src.TraceID,
			OccurredAt:    src.OccurredAt,
			Timestamp:     src.Timestamp,
			Payload:       src.Payload,
		}
		if err := storeB.Append(context.Background(), replay); err != nil {
			t.Fatalf("storeB Append seq %d: %v", seq, err)
		}
		// AssertEntryRoundTrip uses reflect to walk every exported, non-store-
		// assigned Entry field — adding a new field anywhere on Entry picks up
		// here automatically.
		gotB, err := storeB.GetBySeq(context.Background(), seq)
		if err != nil {
			t.Fatalf("storeB GetBySeq(%d): %v", seq, err)
		}
		AssertEntryRoundTrip(t, src, gotB)
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
	// Tail hash parity proves the full 12-field HMAC chain reconstructed
	// byte-for-byte across the two stores. A dropped field on the replay path
	// would break this even when SeqNo / EntryCount still match.
	if tailA.PrevHash != tailB.PrevHash {
		t.Errorf("restart Tail hash mismatch: A=%s B=%s (a canonical-HMAC field was lost on replay)",
			tailA.PrevHash, tailB.PrevHash)
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

// runIdempotencyDifferentTimestampSameEventID: same EventID with different
// Timestamp must be detected as a duplicate (F-CR-2 regression guard).
//
// At-least-once outbox redelivery produces the same EventID but a new clk.Now()
// timestamp on each attempt. The old multi-field fingerprint (eventID + eventType
// + actorID + timestamp + payload) would produce a different fingerprint each
// time and allow the same event to be appended multiple times. The EventID-only
// fingerprint detects the duplicate regardless of the timestamp difference.
func runIdempotencyDifferentTimestampSameEventID(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e1 := &ledger.Entry{
		EventID:   "idem-timestamp-evt",
		EventType: "idempotency.timestamp.test",
		ActorID:   "actor",
		Timestamp: fc.Now(),
		Payload:   []byte(`{"key":"value"}`),
	}
	if err := store.Append(context.Background(), e1); err != nil {
		t.Fatalf("first Append: %v", err)
	}

	// Advance clock to simulate at-least-once redelivery with a new timestamp.
	fc.Advance(redeliveryAdvance)

	// Second append with the same EventID but different Timestamp (simulating
	// outbox relay redelivery). Must return ErrAuditLedgerAlreadyExists.
	e2 := &ledger.Entry{
		EventID:   e1.EventID, // same stable identity
		EventType: e1.EventType,
		ActorID:   e1.ActorID,
		Timestamp: fc.Now(), // different timestamp — redelivery
		Payload:   e1.Payload,
	}
	err := store.Append(context.Background(), e2)
	if err == nil {
		t.Fatal("expected ErrAuditLedgerAlreadyExists for same EventID with different Timestamp")
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
		t.Fatalf(msgVerify, err)
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
			t.Fatalf(msgAppendIdx, i, err)
		}
	}

	valid, firstInvalid, err := store.Verify(context.Background(), 1, 5)
	if err != nil {
		t.Fatalf(msgVerify, err)
	}
	if !valid {
		t.Errorf("Verify: expected valid, first invalid at seq %d", firstInvalid)
	}
}

// runQueryByFilters: Query returns only entries matching the filter.
func runQueryByFilters(t *testing.T, factory Factory) {
	const (
		filterEventType = "type.X" // event type asserted by the EventType filter
		filterSubject   = "alice"  // subject asserted by the SubjectID filter (#1290)
	)
	store, fc, cleanup := factory(t)
	defer cleanup()

	for i := 1; i <= 6; i++ {
		et := filterEventType
		if i > 3 {
			et = "type.Y"
		}
		// SubjectID is orthogonal to EventType: entries 1,2 target filterSubject,
		// the rest target "bob" — exercising the subject_id filter (#1290).
		subject := "bob"
		if i <= 2 {
			subject = filterSubject
		}
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("qf-%d", i),
			EventType: et,
			ActorID:   "actor",
			SubjectID: subject,
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf(msgAppendIdx, i, err)
		}
	}

	results, err := store.Query(context.Background(), ledger.AuditFilters{EventType: filterEventType},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query(type.X): got %d, want 3", len(results))
	}

	// #1290: subject_id filter narrows to the two alice-subject rows.
	bySubject, err := store.Query(context.Background(), ledger.AuditFilters{SubjectID: filterSubject},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(subjectId): %v", err)
	}
	if len(bySubject) != 2 {
		t.Errorf("Query(subjectId=alice): got %d, want 2", len(bySubject))
	}

	// Combined EventType + SubjectID — both predicates AND together.
	combined, err := store.Query(context.Background(),
		ledger.AuditFilters{EventType: filterEventType, SubjectID: filterSubject},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(eventType+subjectId): %v", err)
	}
	if len(combined) != 2 {
		t.Errorf("Query(type.X + subjectId=alice): got %d, want 2", len(combined))
	}
}

// runAppendMultiKeyPayloadRoundTrip verifies that Append → GetBySeq → Verify
// returns the payload bytes byte-for-byte identical to what was supplied.
//
// A-01 regression guard: PG JSONB normalizes key order and strips whitespace,
// so a multi-key payload with non-alphabetical key order and embedded whitespace
// is stored differently from the original bytes, breaking the HMAC hash chain.
// MemStore passes this test (no normalization); PG store FAILS until BYTEA fix.
func runAppendMultiKeyPayloadRoundTrip(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	// Payload with non-alphabetical key order + embedded whitespace.
	// PG JSONB normalizes this to {"a":2,"b":1,"c":"x"}, breaking byte equality.
	payload := []byte(`{"b": 1,"a":2 , "c": "x"}`)
	e := &ledger.Entry{
		EventID:   "multi-key-evt",
		EventType: "multi.key.test",
		ActorID:   "actor",
		Timestamp: fc.Now(),
		Payload:   payload,
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf(msgAppend, err)
	}

	got, err := store.GetBySeq(context.Background(), 1)
	if err != nil {
		t.Fatalf(fmtErrGetBySeq1, err)
	}
	// A-01: payload bytes must be preserved exactly as supplied — no JSONB normalization.
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload byte mismatch:\n  got:  %q\n  want: %q\n  (A-01: JSONB normalization breaks hash chain)",
			got.Payload, payload)
	}

	// Verify must succeed: the hash was computed over the original payload bytes.
	valid, firstInvalid, err := store.Verify(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf(msgVerify, err)
	}
	if !valid {
		t.Errorf("Verify: chain invalid at seq %d after multi-key payload round-trip", firstInvalid)
	}
}

// runQueryOrderingTimestampDescIDAsc verifies that Query returns entries sorted
// by timestamp DESC as the primary key (cross-backend contract).
//
// F-05 regression guard: MemStore.Query previously returned entries in SeqNo
// ascending insertion order, which differs from PG's ORDER BY timestamp DESC.
// This case asserts both backends produce the same timestamp-DESC ordering.
//
// Note on id-ASC tie-break: PG ORDER BY uses `id` (a uuid.NewString() per row)
// for tie-break — that's a PG implementation detail for query stability, NOT
// part of the cross-backend contract. MemStore's Entry.ID (assigned from
// EventID) and PG's Entry.ID (random UUID) cannot agree on tie-break ordering
// by construction, so this case uses 4 distinct timestamps to eliminate ties
// and validate only the timestamp-DESC contract that both backends must honor.
func runQueryOrderingTimestampDescIDAsc(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	base := fc.Now()
	// Seed 4 entries with distinct timestamps; insertion order ≠ desired order
	// so a backend that returns insertion order (the F-05 RED state) fails.
	entries := []struct {
		id    string
		delta time.Duration
	}{
		{"ord-c", testtime.D3s},  // T3
		{"ord-a", testtime.D1s},  // T1 — oldest
		{"ord-d", testtime.D10s}, // T10 — latest
		{"ord-b", testtime.D2s},  // T2
	}
	for _, en := range entries {
		e := &ledger.Entry{
			EventID:   "evt-" + en.id,
			EventType: "order.test",
			ActorID:   "actor",
			Timestamp: base.Add(en.delta),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append %s: %v", en.id, err)
		}
	}

	results, err := store.Query(context.Background(), ledger.AuditFilters{},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("Query: got %d results, want 4", len(results))
	}

	// Expected order: T4(ord-d), T3(ord-c), T2(ord-b), T1(ord-a) — timestamp DESC.
	wantOrder := []string{"ord-d", "ord-c", "ord-b", "ord-a"}
	for i, want := range wantOrder {
		gotEventID := results[i].EventID
		wantEventID := "evt-" + want
		if gotEventID != wantEventID {
			t.Errorf("Query order[%d]: got EventID=%q, want EventID=%q "+
				"(F-05: both backends must sort timestamp DESC)",
				i, gotEventID, wantEventID)
		}
	}
}

// runQueryKeysetPagination verifies cross-backend keyset cursor pagination:
// full traversal via the (timestamp DESC, id ASC) keyset yields every entry
// exactly once, in timestamp-DESC order, with the final page detected by
// len(rows) <= limit (no FetchLimit overflow row).
//
// Sub-second-distinct timestamps are deliberately used: the cursor value for
// the timestamp column is an RFC3339Nano *string* (the wire form produced by
// the service Extract closure after JSON round-trip). The PG store must convert
// it back to time.Time before pushing it into the timestamptz keyset predicate;
// millisecond-distinct timestamps prove that conversion preserves sub-second
// ordering rather than truncating or lexically mis-comparing. MemStore exercises
// the same string-typed cursor through query.CompareAny.
//
// The suite calls store.Query directly (raw CursorValues, not an opaque codec
// token), so the next page's cursor is built by hand from the last visible
// entry — mirroring the service Extract: []any{ts.Format(RFC3339Nano), id}.
func runQueryKeysetPagination(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	seedKeysetEntries(t, store, fc.Now())
	collected := collectKeysetPages(t, store, 2)

	// timestamp DESC → ks-6, ks-5, ..., ks-0 (no ties: every entry seen once).
	want := []string{"ks-6", "ks-5", "ks-4", "ks-3", "ks-2", "ks-1", "ks-0"}
	if len(collected) != len(want) {
		t.Fatalf("keyset traversal: got %d entries %v, want %d %v",
			len(collected), collected, len(want), want)
	}
	for i := range want {
		if collected[i] != want[i] {
			t.Errorf("keyset order[%d]: got %q, want %q (full list got=%v)",
				i, collected[i], want[i], collected)
		}
	}
}

// keysetSeedTotal is the number of entries seeded by seedKeysetEntries and the
// upper bound on collectKeysetPages iterations.
const keysetSeedTotal = 7

// seedKeysetEntries appends keysetSeedTotal entries (ks-0..ks-6) in shuffled
// insertion order with millisecond-distinct timestamps, so a backend that
// ignores the keyset (returns insertion order) is caught.
func seedKeysetEntries(t *testing.T, store ledger.Store, base time.Time) {
	t.Helper()
	insertion := []int{3, 0, 5, 1, 6, 2, 4}
	for _, n := range insertion {
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("ks-%d", n),
			EventType: "keyset.test",
			ActorID:   "actor",
			Timestamp: base.Add(time.Duration(n) * time.Millisecond),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append ks-%d: %v", n, err)
		}
	}
}

// collectKeysetPages traverses every page via the (timestamp DESC, id ASC)
// keyset and returns the EventIDs in visit order. Each next-page cursor is
// built by hand from the last visible entry — mirroring the service Extract:
// []any{ts.Format(RFC3339Nano), id} — to reproduce the string-typed cursor that
// exercises the PG timestamptz bind path. The loop is bounded by keysetSeedTotal
// to guard against a non-terminating cursor.
//
// This drives the store-layer CursorValues API directly (raw []any), not an
// opaque codec token — codec round-tripping is covered by the service tests.
// last.ID differs by backend (EventID on MemStore, random UUID on PG) but is
// always taken from the Query result, so the cross-backend contract asserted
// here is "full traversal visits every entry once in order", not cursor-value
// equality (the seed uses distinct timestamps so the id tie-break never fires).
func collectKeysetPages(t *testing.T, store ledger.Store, limit int) []string {
	t.Helper()
	var collected []string
	var cursorVals []any
	for iter := 0; iter <= keysetSeedTotal+1; iter++ {
		rows, err := store.Query(context.Background(), ledger.AuditFilters{},
			query.ListParams{Limit: limit, Sort: ledger.QuerySort(), CursorValues: cursorVals})
		if err != nil {
			t.Fatalf("Query iter %d: %v", iter, err)
		}
		page := rows
		hasMore := len(rows) > limit
		if hasMore {
			page = rows[:limit]
		}
		for _, e := range page {
			collected = append(collected, e.EventID)
		}
		if !hasMore {
			return collected
		}
		last := page[len(page)-1]
		cursorVals = []any{last.Timestamp.Format(time.RFC3339Nano), last.ID}
	}
	t.Fatal("keyset traversal did not terminate within bound")
	return nil
}

// runQueryEmptySortRejected asserts both backends reject a Query with an empty
// Sort with ErrValidationFailed. The keyset contract requires a non-empty sort
// (MemStore guards explicitly; PG via pgquery.AppendKeyset) — this case locks
// that both backends produce the same rejection.
func runQueryEmptySortRejected(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	_, err := store.Query(context.Background(), ledger.AuditFilters{}, query.ListParams{Limit: 10})
	assertErrCode(t, err, errcode.ErrValidationFailed)
}

// runQueryInvalidCursorRejected asserts both backends reject a cursor whose
// timestamp value is not a valid RFC3339Nano string with ErrCursorInvalid.
// The backends reach the rejection via different paths — MemStore through
// query.CompareAny's parse, PG through bindTimestampCursor's time.Parse — so
// this case locks cross-backend parity on the malformed-cursor error. At least
// one entry is seeded so MemStore's ApplyCursor actually performs the compare.
func runQueryInvalidCursorRejected(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	for i := 1; i <= 2; i++ {
		e := NewEntryFixture(t, fmt.Sprintf("badcur-%d", i), "badcur.test", "actor", fc.Now())
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf(msgAppendIdx, i, err)
		}
	}

	_, err := store.Query(context.Background(), ledger.AuditFilters{},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort(), CursorValues: []any{"not-a-timestamp", "some-id"}})
	assertErrCode(t, err, errcode.ErrCursorInvalid)
}

// runProtocolHashParity verifies that a Store's persisted entry.Hash matches
// protocol.ComputeHash byte-for-byte. This is the single-source contract
// between Store implementations and Protocol: both Mem and PG stores must
// produce identical hashes for identical inputs, otherwise chain continuity
// breaks when a payload migrates across backends or a Verify spans the cut.
//
// Two entries cover both prevHash branches:
//   - seq 1: prevHash="" (chain root)
//   - seq 2: prevHash=entry1.Hash (chain link)
func runProtocolHashParity(t *testing.T, factory Factory, protocol *ledger.Protocol) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	e1 := NewEntryFixture(t, "parity-1", "parity.test", "actor-parity", fc.Now())
	if err := store.Append(context.Background(), e1); err != nil {
		t.Fatalf("Append seq 1: %v", err)
	}
	e2 := NewEntryFixture(t, "parity-2", "parity.test", "actor-parity", fc.Now())
	if err := store.Append(context.Background(), e2); err != nil {
		t.Fatalf("Append seq 2: %v", err)
	}

	got1, err := store.GetBySeq(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetBySeq 1: %v", err)
	}
	want1 := protocol.ComputeHash("", got1)
	if got1.Hash != want1 {
		t.Errorf("seq 1 hash parity broken: store=%s protocol=%s "+
			"(Store implementation and Protocol must agree byte-for-byte)",
			got1.Hash, want1)
	}

	got2, err := store.GetBySeq(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetBySeq 2: %v", err)
	}
	want2 := protocol.ComputeHash(got1.Hash, got2)
	if got2.Hash != want2 {
		t.Errorf("seq 2 hash parity broken: store=%s protocol=%s "+
			"(chain link broken — Store does not use Protocol.ComputeHash for prevHash threading)",
			got2.Hash, want2)
	}
}

// principalOccurredAtSkew is the producer-clock skew used by
// RunPrincipalFieldsRoundTrip to set Entry.OccurredAt distinct from
// Entry.Timestamp. The value is a fixture offset only — extracted to a
// package-level const per TEST-TIME-LITERAL-01.
const principalOccurredAtSkew = -30 * time.Second

// referenceHashInput mirrors the unexported runtime/audit/ledger.auditHashInput
// struct used by Protocol.ComputeHash. Hash-parity tests must not call
// Protocol.ComputeHash for their reference value — that would devolve into
// production-versus-production circular verification, unable to catch a silent
// dropped-field regression in ComputeHash itself. This independent mirror
// recomputes the canonical HMAC byte-for-byte from the same Entry fields.
//
// Drift between this struct and runtime/audit/ledger.auditHashInput surfaces
// twice: (a) here as a test failure when the store-persisted Hash diverges
// from the reference, (b) in archtest AUDIT-HASH-INPUT-FROZEN-01 A1 which
// reflect-locks the production struct.
type referenceHashInput struct {
	Namespace          string `json:"namespace"`
	PrevHash           string `json:"prev_hash"`
	EventID            string `json:"event_id"`
	EventType          string `json:"event_type"`
	ActorID            string `json:"actor_id"`
	SubjectID          string `json:"subject_id"`
	TenantID           string `json:"tenant_id"`
	SessionID          string `json:"session_id"`
	CorrelationID      string `json:"correlation_id"`
	OccurredAtUnixNano int64  `json:"occurred_at_unix_nano"`
	TimestampUnixNano  int64  `json:"timestamp_unix_nano"`
	Payload            []byte `json:"payload"`
}

// ReferenceComputeHash recomputes the canonical HMAC for an Entry without
// touching Protocol.ComputeHash, serving as the single independent oracle for
// every audit-ledger hash-parity test (both the storetest suite and the
// ledger-package white-box tests call it — there is no second copy). Callers
// supply the HMAC key, the namespace, and the expected prev_hash; the function
// marshals the mirror struct and returns the hex digest. The namespace is the
// first signed field, mirroring the production cross-namespace HMAC domain
// separation (ADR-1042 §A).
//
// The marshal cannot fail for referenceHashInput (only string/int64/[]byte
// fields), but the error is surfaced via t.Fatalf rather than discarded — the
// project forbids silently dropping errors, and a future field of an
// unmarshalable type must fail loudly.
func ReferenceComputeHash(t testing.TB, key []byte, ns ledger.NamespaceID, prevHash string, e *ledger.Entry) string {
	t.Helper()
	in := referenceHashInput{
		Namespace:          string(ns),
		PrevHash:           prevHash,
		EventID:            e.EventID,
		EventType:          e.EventType,
		ActorID:            e.ActorID,
		SubjectID:          e.SubjectID,
		TenantID:           e.TenantID,
		SessionID:          e.SessionID,
		CorrelationID:      e.CorrelationID,
		OccurredAtUnixNano: e.OccurredAt.UnixNano(),
		TimestampUnixNano:  e.Timestamp.UnixNano(),
		Payload:            e.Payload,
	}
	msgBytes, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("ReferenceComputeHash: marshal referenceHashInput: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(msgBytes)
	return hex.EncodeToString(mac.Sum(nil))
}

// RunPrincipalFieldsRoundTrip asserts that the five canonical Principal /
// Correlation / OccurredAt fields added in migration 043_audit_entries_v2 round
// trip through Append → GetBySeq with byte-equal values, and that the persisted
// Hash matches protocol.ComputeHash on the populated entry. Combined with
// Protocol_HashParity it forms the single-source HMAC parity contract: any
// Store implementation must (a) persist all 11 input fields losslessly and
// (b) compute Hash via Protocol.ComputeHash so identical inputs map to
// identical hashes across MemStore / PG Store / future backends.
//
// Wired into Run() as the "PrincipalFields_RoundTrip" subtest; also exported
// for direct invocation from per-backend tests that wish to call it outside the
// full Run() suite.
func RunPrincipalFieldsRoundTrip(t *testing.T, factory Factory, protocol *ledger.Protocol) {
	t.Helper()
	if factory == nil {
		t.Fatal("storetest.RunPrincipalFieldsRoundTrip: factory must not be nil")
	}
	if protocol == nil {
		t.Fatal("storetest.RunPrincipalFieldsRoundTrip: protocol must not be nil")
	}

	store, fc, cleanup := factory(t)
	defer cleanup()

	occurredAt := fc.Now().Add(principalOccurredAtSkew).UTC()
	entry := &ledger.Entry{
		EventID:       "principal-roundtrip",
		EventType:     "user.login",
		ActorID:       "actor-impersonator",
		SubjectID:     "subject-end-user",
		TenantID:      "tenant-alpha",
		SessionID:     "sess-42",
		CorrelationID: "corr-xyz-001",
		OccurredAt:    occurredAt,
		Timestamp:     fc.Now(),
		Payload:       []byte(`{"action":"login"}`),
	}

	if err := store.Append(context.Background(), entry); err != nil {
		t.Fatalf("Append principal-roundtrip: %v", err)
	}

	got, err := store.GetBySeq(context.Background(), 1)
	if err != nil {
		t.Fatalf(fmtErrGetBySeq1, err)
	}

	// AssertEntryRoundTrip walks every caller-supplied Entry field via reflect,
	// so the 5 Principal/Correlation/OccurredAt fields plus any future
	// expansion are checked without per-field maintenance here. Drift between
	// Entry's field set and AUDIT-HASH-INPUT-FROZEN-01 A1 surfaces in that
	// archtest first; this assertion surfaces it as a runtime parity failure
	// against the same surface.
	AssertEntryRoundTrip(t, entry, got)

	// HMAC parity: store-persisted Hash must equal an INDEPENDENT canonical
	// HMAC over the loaded entry — recomputed via ReferenceComputeHash, which
	// marshals an external mirror struct (referenceHashInput) and never calls
	// Protocol.ComputeHash. A regression that silently drops a field from
	// ComputeHash (e.g. forgetting to wire SubjectID into auditHashInput)
	// would still produce a self-consistent hash via protocol.ComputeHash;
	// the independent reference catches it.
	want := ReferenceComputeHash(t, TestHMACKey(), protocol.Namespace(), "", got)
	if got.Hash != want {
		t.Errorf("Hash parity broken with populated Principal fields:\n  store=%s\n  ref  =%s",
			got.Hash, want)
	}
}

// runQueryByTraceID verifies that Query with AuditFilters{TraceID:...} returns
// only entries whose trace_id matches, and that an empty TraceID filter matches
// all entries. Cross-backend contract: every Store must index or scan by
// trace_id correctly so operational trace correlation works uniformly.
//
// Three entries are seeded: two share the same trace_id ("trace-abc") and one
// has a distinct trace_id ("trace-xyz"). The assertions mirror
// TestMemStore_Query_ByTraceID in mem_store_test.go and the one-off PG
// integration test, consolidating both into the shared conformance suite.
func runQueryByTraceID(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	seed := []struct {
		eventID string
		traceID string
	}{
		{"trace-suite-1", "trace-abc"},
		{"trace-suite-2", "trace-abc"},
		{"trace-suite-3", "trace-xyz"},
	}
	for _, s := range seed {
		e := &ledger.Entry{
			EventID:   s.eventID,
			EventType: "trace.suite.test",
			ActorID:   "actor",
			TraceID:   s.traceID,
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append %s: %v", s.eventID, err)
		}
	}

	// Exact-match filter — must return the two "trace-abc" entries only.
	results, err := store.Query(context.Background(),
		ledger.AuditFilters{TraceID: "trace-abc"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(TraceID=trace-abc): %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Query(TraceID=trace-abc): got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.TraceID != "trace-abc" {
			t.Errorf("Query(TraceID=trace-abc) returned unexpected TraceID %q", r.TraceID)
		}
	}

	// Empty TraceID filter must return all 3 entries.
	all, err := store.Query(context.Background(),
		ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(empty TraceID): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Query(empty TraceID): got %d results, want 3", len(all))
	}
}

// runTraceIDNotInHashChain verifies that two entries differing ONLY in TraceID
// produce the SAME Protocol.ComputeHash value. This proves trace_id is excluded
// from the HMAC chain so the presence or absence of trace context does not
// invalidate historical entries or break chain continuity across instrumented
// and non-instrumented code paths.
//
// The invariant is cross-backend: every future Store implementation must honor
// it because chain integrity depends on trace_id being a non-chained
// observability field. Consolidates TestMemStore_TraceID_NotInHashChain
// (mem_store_test.go) into the shared suite.
func runTraceIDNotInHashChain(t *testing.T, factory Factory, protocol *ledger.Protocol) {
	// Consume factory first so backend resources (e.g. PG schema reset) are
	// allocated and released via cleanup before any protocol assertions.
	_, _, cleanup := factory(t)
	defer cleanup()

	// This case exercises Protocol.ComputeHash directly rather than Store.Append
	// so that the non-chained invariant is proven at the protocol layer, not just
	// at the storage layer. A storage backend that silently dropped TraceID from
	// the HMAC input would produce the same hashes — and that is exactly the
	// correct behavior we are asserting.
	baseTs := epochAnchor

	withTrace := &ledger.Entry{
		EventID:   "suite-hash-chain-evt",
		EventType: "hash.chain.suite.test",
		ActorID:   "actor-1",
		TraceID:   "4bf92f3577b34da6a3ce929d0e0e4736",
		Timestamp: baseTs,
		Payload:   []byte(`{"a":1}`),
	}
	withoutTrace := &ledger.Entry{
		EventID:   "suite-hash-chain-evt",
		EventType: "hash.chain.suite.test",
		ActorID:   "actor-1",
		TraceID:   "",
		Timestamp: baseTs,
		Payload:   []byte(`{"a":1}`),
	}

	hashWith := protocol.ComputeHash("", withTrace)
	hashWithout := protocol.ComputeHash("", withoutTrace)

	if hashWith != hashWithout {
		t.Errorf("TraceID must NOT affect HMAC hash (non-chained observability invariant):\n"+
			"  hash(with trace)    = %s\n  hash(without trace) = %s",
			hashWith, hashWithout)
	}
}

// NOTE: assertErrCode is reserved for non-_NotFound assertions
// (ErrAuditLedgerAlreadyExists, ErrValidationFailed). Any t.Run("..._NotFound", ...)
// table case MUST call errcodetest.AssertCode directly — POSTGRES-NOTFOUND-
// TEST-OTHER-ERROR-MIXUP-ARCHTEST-01 archtest detects funnel calls at the
// _NotFound t.Run body level only; routing through this helper would be a
// cross-function wrapper that escapes detection (godoc-declared blind spot).
//
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
