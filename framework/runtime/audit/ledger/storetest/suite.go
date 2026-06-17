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

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest" // test funnel; storetest is testing-helper package, errcodetest import is intentional (not a test-only import in a non-_test.go file)
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

const (
	appendEventErrFmt = "Append %s: %v"
	tenantNoneEventID = "ti-none"
	chainAEventID     = "chain-a-1"
	chainBEventID     = "chain-b-1"
)

const fmtErrGetBySeq1 = "GetBySeq(1): %v"

// passthroughTxRunner is a no-op TxRunner used by MemStore conformance tests.
// MemStore has no pool defense — scope-bearing contexts flow through without a
// real DB transaction — so fn is called directly without wrapping.
type passthroughTxRunner struct{}

func (passthroughTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// PassthroughTxRunner returns a persistence.TxRunner that calls fn(ctx)
// directly without wrapping in a real database transaction. Use this in
// MemStore-backed Factory implementations where no pool defense exists.
func PassthroughTxRunner() persistence.TxRunner { return passthroughTxRunner{} }

// scopedTail wraps a tenant-scoped Tail in RunInTx so that PG's deep-defense
// (PrepareConn: reject scope-bearing ctx outside RunInTx) does not fire.
// For MemStore, passthroughTxRunner simply calls fn(ctx) directly.
func scopedTail(t *testing.T, tr persistence.TxRunner, store ledger.Store, tid tenant.TenantID) (ledger.TailSnapshot, error) {
	t.Helper()
	var snap ledger.TailSnapshot
	var rerr error
	err := tr.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		snap, rerr = store.Tail(ctx)
		return rerr
	})
	if err != nil {
		return ledger.TailSnapshot{}, err
	}
	return snap, nil
}

// scopedGetBySeq wraps a tenant-scoped GetBySeq in RunInTx for the same reason
// as scopedTail — PG pool defense rejects scope-bearing ctx outside RunInTx.
func scopedGetBySeq(
	t *testing.T, tr persistence.TxRunner, store ledger.Store,
	tid tenant.TenantID, vis tenant.RowVisibility, seq int64,
) (*ledger.Entry, error) {
	t.Helper()
	var entry *ledger.Entry
	var rerr error
	err := tr.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		entry, rerr = store.GetBySeq(ctx, vis, seq)
		return rerr
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// scopedVerify wraps a tenant-scoped Verify in RunInTx for the same reason.
func scopedVerify(t *testing.T, tr persistence.TxRunner, store ledger.Store, tid tenant.TenantID, from, to int64) (bool, int64, error) {
	t.Helper()
	var valid bool
	var firstInvalid int64
	var rerr error
	err := tr.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		valid, firstInvalid, rerr = store.Verify(ctx, from, to)
		return rerr
	})
	if err != nil {
		return false, 0, err
	}
	return valid, firstInvalid, nil
}

// mustRowVisibility constructs a tenant.RowVisibility for test use, failing the
// test if the arguments are invalid. Most conformance callsites use
// tenant.RowScopeTenant with an empty subject (unrestricted visibility) so that
// existing tests observe the same behavior as before PR-4. New visibility-
// obligation tests pass explicit scope/subject values.
//
// RowScopeAll is minted via the sealed tenant.NewCrossTenantVisibility funnel
// (#1760: tenant.NewRowVisibility rejects All); the conformance suite still needs
// an All obligation to assert every serving store fail-closes it
// (RowScopeAllUnsupportedError). This is the only other allowlisted caller of
// NewCrossTenantVisibility besides the runtime/auth super-admin derivation
// (ROWSCOPEALL-AUDIT-FUNNEL-01) — sanctioned because it is a testing-helper pkg.
func mustRowVisibility(t testing.TB, scope tenant.RowScope, subject string) tenant.RowVisibility {
	t.Helper()
	if scope == tenant.RowScopeAll {
		return tenant.NewCrossTenantVisibility().Visibility()
	}
	v, err := tenant.NewRowVisibility(scope, subject)
	if err != nil {
		t.Fatalf("mustRowVisibility(%v, %q): %v", scope, subject, err)
	}
	return v
}

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
//
// txRunner is used by suite helpers to wrap tenant-scoped Tail/GetBySeq/Verify
// calls in RunInTx so that the PG pool's deep-defense (PrepareConn: reject
// scope-bearing ctx outside RunInTx) does not fire. MemStore factories return
// passthroughTxRunner which calls fn(ctx) directly.
type Factory func(t *testing.T) (store ledger.Store, txRunner persistence.TxRunner, fakeClock *clockmock.FakeClock, cleanup func())

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
// Conformance tenant identifiers MUST be canonical UUIDs (#1618): scoped chain
// reads (GetBySeq/Verify/Tail) run inside RunInTx, and the PG TxManager's
// setLocalTenant validates the RLS GUC tenant via tenant.TenantID.Validate
// (canonical 36-char dashed UUID, non-nil). conformanceTenant is the standard
// single-tenant fixture chain; conformanceTenantA/B are the distinct per-tenant
// chains exercised by Per_Tenant_Chains.
const (
	conformanceTenant  = "11111111-1111-1111-1111-111111111111"
	conformanceTenantA = "22222222-2222-2222-2222-222222222222"
	conformanceTenantB = "33333333-3333-3333-3333-333333333333"
)

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
		TenantID:      conformanceTenant,
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
	t.Run("Idempotency_PerTenant_EventID", func(t *testing.T) { runIdempotencyPerTenantEventID(t, factory) })
	t.Run("Concurrent_Append_HashChainValid", func(t *testing.T) { runConcurrentAppendHashChainValid(t, factory) })
	t.Run("StrictPayload_InvalidJSON", func(t *testing.T) { runStrictPayloadInvalidJSON(t, factory) })
	t.Run("Verify_FullRange", func(t *testing.T) { runVerifyFullRange(t, factory) })
	t.Run("GetBySeq_NotFound", func(t *testing.T) {
		store, _, _, cleanup := factory(t)
		defer cleanup()
		_, err := store.GetBySeq(context.Background(), mustRowVisibility(t, tenant.RowScopeTenant, ""), 9999)
		errcodetest.AssertCode(t, err, errcode.ErrAuditLedgerNotFound)
	})
	t.Run("Query_ByFilters", func(t *testing.T) { runQueryByFilters(t, factory) })
	t.Run("Append_MultiKey_Payload_RoundTrip", func(t *testing.T) { runAppendMultiKeyPayloadRoundTrip(t, factory) })
	t.Run("Query_Ordering_TimestampDesc_IDAsc", func(t *testing.T) { runQueryOrderingTimestampDescIDAsc(t, factory) })
	t.Run("Query_Keyset_Pagination", func(t *testing.T) { runQueryKeysetPagination(t, factory) })
	t.Run("Query_EmptySort_Rejected", func(t *testing.T) { runQueryEmptySortRejected(t, factory) })
	t.Run("Query_InvalidCursor_Rejected", func(t *testing.T) { runQueryInvalidCursorRejected(t, factory) })
	t.Run("Query_TenantIsolation", func(t *testing.T) { runQueryTenantIsolation(t, factory) })
	t.Run("Per_Tenant_Chains", func(t *testing.T) { runPerTenantChains(t, factory) })
	t.Run("Protocol_HashParity", func(t *testing.T) { runProtocolHashParity(t, factory, protocol) })
	t.Run("PrincipalFields_RoundTrip", func(t *testing.T) { RunPrincipalFieldsRoundTrip(t, factory, protocol) })
	t.Run("Query_ByTraceID", func(t *testing.T) { runQueryByTraceID(t, factory) })
	t.Run("Query_VisibilityObligations", func(t *testing.T) { runQueryVisibilityObligations(t, factory) })
	t.Run("GetBySeq_VisibilityObligations", func(t *testing.T) { runGetBySeqVisibilityObligations(t, factory) })
	t.Run("Query_InvalidCharFilter_Rejected", func(t *testing.T) { runQueryInvalidCharFilterRejected(t, factory) })
	t.Run("GetByID_NotFound", func(t *testing.T) {
		store, _, _, cleanup := factory(t)
		defer cleanup()
		_, err := store.GetByID(context.Background(), tenant.TenantID(conformanceTenant),
			mustRowVisibility(t, tenant.RowScopeTenant, ""), getByIDMissingID)
		errcodetest.AssertCode(t, err, errcode.ErrAuditLedgerNotFound)
	})
	t.Run("GetByID_MalformedID_NotFound", func(t *testing.T) {
		// A non-uuid id collapses to not-found on every backend (PG parse-guards it
		// off the $::uuid cast; mem finds no matching id) — never a 22P02 / 500 leak.
		store, _, _, cleanup := factory(t)
		defer cleanup()
		_, err := store.GetByID(context.Background(), tenant.TenantID(conformanceTenant),
			mustRowVisibility(t, tenant.RowScopeTenant, ""), getByIDMalformedID)
		errcodetest.AssertCode(t, err, errcode.ErrAuditLedgerNotFound)
	})
	t.Run("GetByID_VisibilityObligations", func(t *testing.T) { runGetByIDVisibilityObligations(t, factory) })
	t.Run("GetByID_CrossTenantHidden", func(t *testing.T) { runGetByIDCrossTenantHidden(t, factory) })
}

// runAppendTailRoundTrip: Append persists entry; Tail advances; GetBySeq returns entry.
func runAppendTailRoundTrip(t *testing.T, factory Factory) {
	store, tr, fc, cleanup := factory(t)
	defer cleanup()

	e := NewEntryFixture(t, "evt-round-trip", "audit.test", "actor-1", fc.Now())
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf(msgAppend, err)
	}

	fixtureTenant := tenant.TenantID(conformanceTenant)
	tail, err := scopedTail(t, tr, store, fixtureTenant)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if tail.SeqNo != 1 {
		t.Errorf("Tail.SeqNo: got %d, want 1", tail.SeqNo)
	}
	if tail.EntryCount != 1 {
		t.Errorf("Tail.EntryCount: got %d, want 1", tail.EntryCount)
	}

	got, err := scopedGetBySeq(t, tr, store, fixtureTenant, mustRowVisibility(t, tenant.RowScopeTenant, ""), 1)
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
	store, _, _, cleanup := factory(t)
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
	storeA, trA, fc, cleanupA := factory(t)
	defer cleanupA()

	const n = 3
	for i := 1; i <= n; i++ {
		e := NewEntryFixture(t, fmt.Sprintf("restart-evt-%d", i), "restart.test", "actor", fc.Now())
		if err := storeA.Append(context.Background(), e); err != nil {
			t.Fatalf("storeA Append %d: %v", i, err)
		}
	}
	fixtureTenant := tenant.TenantID(conformanceTenant)
	tailA, err := scopedTail(t, trA, storeA, fixtureTenant)
	if err != nil {
		t.Fatalf("storeA Tail: %v", err)
	}

	// Replay into storeB using a second factory call. Replay copies the FULL
	// Entry shape (every caller-supplied field) so the cross-store chain truly
	// witnesses all 12 canonical-HMAC fields, not just the historical 5.
	storeB, trB, _, cleanupB := factory(t)
	defer cleanupB()

	vis := mustRowVisibility(t, tenant.RowScopeTenant, "")
	for seq := int64(1); seq <= int64(n); seq++ {
		src, err := scopedGetBySeq(t, trA, storeA, fixtureTenant, vis, seq)
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
		gotB, err := scopedGetBySeq(t, trB, storeB, fixtureTenant, vis, seq)
		if err != nil {
			t.Fatalf("storeB GetBySeq(%d): %v", seq, err)
		}
		AssertEntryRoundTrip(t, src, gotB)
	}
	tailB, err := scopedTail(t, trB, storeB, fixtureTenant)
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
	store, _, fc, cleanup := factory(t)
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
	store, _, fc, cleanup := factory(t)
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

// runIdempotencyPerTenantEventID pins the per-(namespace, tenant) idempotency
// boundary introduced by migration 055 (UNIQUE(namespace, tenant_id, event_id),
// #1618). The dedup key gained a tenant_id axis, so the SAME EventID:
//
//   - conflicts WITHIN one tenant (→ ErrAuditLedgerAlreadyExists), and
//   - is ALLOWED ACROSS tenants — each per-tenant sub-chain dedups independently,
//     so a tenant-B event reusing a tenant-A EventID is not a duplicate.
//
// The pre-055 namespace-global dedup (UNIQUE(namespace, event_id)) would have
// rejected the cross-tenant reuse; this case is the regression guard that the key
// is now per-tenant. Runs on every backend (mem + PG via factory).
func runIdempotencyPerTenantEventID(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	const sharedEventID = "per-tenant-idem-evt"
	mk := func(tenantID string) *ledger.Entry {
		return &ledger.Entry{
			EventID:   sharedEventID,
			EventType: "idempotency.tenant.test",
			ActorID:   "actor",
			TenantID:  tenantID,
			Timestamp: fc.Now(),
			Payload:   []byte(`{"k":"v"}`),
		}
	}

	// Tenant A: first append commits.
	if err := store.Append(context.Background(), mk(conformanceTenantA)); err != nil {
		t.Fatalf("tenant-A first Append: %v", err)
	}
	// Tenant A: same EventID again → duplicate within the tenant chain.
	errDup := store.Append(context.Background(), mk(conformanceTenantA))
	if errDup == nil {
		t.Fatal("tenant-A duplicate EventID: expected ErrAuditLedgerAlreadyExists, got nil")
	}
	assertErrCode(t, errDup, errcode.ErrAuditLedgerAlreadyExists)

	// Tenant B: SAME EventID is allowed — the key is (namespace, tenant_id,
	// event_id), so cross-tenant chains dedup independently.
	if err := store.Append(context.Background(), mk(conformanceTenantB)); err != nil {
		t.Fatalf("tenant-B same EventID must be allowed (per-tenant idempotency): %v", err)
	}
}

// runConcurrentAppendHashChainValid: 100 concurrent appends; chain must be valid.
// F24: increased from 50 to 100 to align with PG integration test concurrency level.
func runConcurrentAppendHashChainValid(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
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
	store, _, fc, cleanup := factory(t)
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
	store, tr, fc, cleanup := factory(t)
	defer cleanup()

	for i := 1; i <= 5; i++ {
		e := NewEntryFixture(t, fmt.Sprintf("vf-%d", i), "verify.test", "actor", fc.Now())
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf(msgAppendIdx, i, err)
		}
	}

	valid, firstInvalid, err := scopedVerify(t, tr, store, tenant.TenantID(conformanceTenant), 1, 5)
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
	store, _, fc, cleanup := factory(t)
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

	// Entries have no TenantID → system/framework chain (""). Query with "" sees all.
	sysVis := mustRowVisibility(t, tenant.RowScopeTenant, "")
	results, err := store.Query(context.Background(), tenant.TenantID(""), sysVis,
		ledger.AuditFilters{EventType: filterEventType},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query(type.X): got %d, want 3", len(results))
	}

	// #1290: subject_id filter narrows to the two alice-subject rows.
	bySubject, err := store.Query(context.Background(), tenant.TenantID(""), sysVis,
		ledger.AuditFilters{SubjectID: filterSubject},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(subjectId): %v", err)
	}
	if len(bySubject) != 2 {
		t.Errorf("Query(subjectId=alice): got %d, want 2", len(bySubject))
	}

	// Combined EventType + SubjectID — both predicates AND together.
	combined, err := store.Query(context.Background(), tenant.TenantID(""), sysVis,
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
	store, _, fc, cleanup := factory(t)
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

	got, err := store.GetBySeq(context.Background(), mustRowVisibility(t, tenant.RowScopeTenant, ""), 1)
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
// Note on id-ASC tie-break: PG ORDER BY uses `id` (a random uuid per row) for
// tie-break — that's a PG implementation detail for query stability, NOT part of
// the cross-backend contract. MemStore's Entry.ID (a deterministic hash of
// namespace+tenant+eventID) and PG's Entry.ID (random uuid) are both globally
// unique but cannot agree on tie-break ORDERING by construction, so this case uses
// 4 distinct timestamps to eliminate ties and validate only the timestamp-DESC
// contract that both backends must honor.
func runQueryOrderingTimestampDescIDAsc(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
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
			t.Fatalf(appendEventErrFmt, en.id, err)
		}
	}

	// Entries have no TenantID → system chain "". Query with "" sees all chains.
	results, err := store.Query(context.Background(), tenant.TenantID(""),
		mustRowVisibility(t, tenant.RowScopeTenant, ""), ledger.AuditFilters{},
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
	store, _, fc, cleanup := factory(t)
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
	// Keyset entries have no TenantID → system chain "". Query with "" sees all.
	tid := tenant.TenantID("")
	vis := mustRowVisibility(t, tenant.RowScopeTenant, "")
	var collected []string
	var cursorVals []any
	for iter := 0; iter <= keysetSeedTotal+1; iter++ {
		rows, err := store.Query(context.Background(), tid, vis, ledger.AuditFilters{},
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
	store, _, _, cleanup := factory(t)
	defer cleanup()

	_, err := store.Query(context.Background(), tenant.TenantID(""), mustRowVisibility(t, tenant.RowScopeTenant, ""),
		ledger.AuditFilters{}, query.ListParams{Limit: 10})
	assertErrCode(t, err, errcode.ErrValidationFailed)
}

// runQueryInvalidCursorRejected asserts both backends reject a cursor whose
// timestamp value is not a valid RFC3339Nano string with ErrCursorInvalid.
// The backends reach the rejection via different paths — MemStore through
// query.CompareAny's parse, PG through bindTimestampCursor's time.Parse — so
// this case locks cross-backend parity on the malformed-cursor error. At least
// one entry is seeded so MemStore's ApplyCursor actually performs the compare.
func runQueryInvalidCursorRejected(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	for i := 1; i <= 2; i++ {
		e := NewEntryFixture(t, fmt.Sprintf("badcur-%d", i), "badcur.test", "actor", fc.Now())
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf(msgAppendIdx, i, err)
		}
	}

	// Entries from NewEntryFixture go to "tenant-test" chain.
	_, err := store.Query(context.Background(), tenant.TenantID(conformanceTenant),
		mustRowVisibility(t, tenant.RowScopeTenant, ""), ledger.AuditFilters{},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort(), CursorValues: []any{"not-a-timestamp", "some-id"}})
	assertErrCode(t, err, errcode.ErrCursorInvalid)
}

// Canonical tenant UUIDs for the per-tenant isolation conformance (#1618). They
// MUST be canonical: Store.Query now rejects a non-empty non-canonical tenant
// (ledger.ValidateQueryTenant), so the pre-#1618 "tenant-a"/"tenant-b" string
// fixtures would fail the contract instead of exercising the partitioning — which
// is exactly the gap review F4 closed (the suite now pins the mandatory canonical
// tenant contract).
const (
	isoTenantA = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	isoTenantB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// runQueryTenantIsolation locks per-(namespace,tenant) chain isolation (#1618):
// querying with tenant-A returns only tenant-A rows + tenant-less system rows
// (never tenant-B's rows), and vice versa. Two tenant-axis edges are also pinned:
//   - an EMPTY tenant is the legitimate system-chain read (ValidateQueryTenant
//     permits it) — it collapses the predicate to `tenant_id = ”` and sees ONLY
//     system rows, never any tenant's rows (mem mirrors the PG predicate exactly);
//   - a NON-EMPTY but non-canonical tenant is rejected at the store, so a garbage
//     tenant can never be treated as a silent distinct partition.
//
// Production always passes a canonical tenant from the authenticated principal
// (and Service.Query additionally rejects an empty one at the post-auth boundary).
func runQueryTenantIsolation(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	// Seed: 2 rows in tenant-A, 1 in tenant-B, 1 tenant-less (system chain "").
	// Same event type so only the tenant partition distinguishes them.
	seed := []struct {
		eventID  string
		tenantID string
	}{
		{"ti-a1", isoTenantA},
		{"ti-a2", isoTenantA},
		{"ti-b1", isoTenantB},
		{tenantNoneEventID, ""},
	}
	for _, s := range seed {
		e := &ledger.Entry{
			EventID:   s.eventID,
			EventType: "tenant.iso.test",
			ActorID:   "actor",
			TenantID:  s.tenantID,
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf(appendEventErrFmt, s.eventID, err)
		}
	}

	cases := []struct {
		name    string
		tid     string
		wantIDs []string
	}{
		// tenant-A: sees its OWN rows + system ("") rows, never tenant-B rows.
		{"tenant-A sees its rows + system, not tenant-B", isoTenantA, []string{"ti-a1", "ti-a2", tenantNoneEventID}},
		// tenant-B: sees its row + system ("") rows, never tenant-A rows.
		{"tenant-B sees its row + system, not tenant-A", isoTenantB, []string{"ti-b1", tenantNoneEventID}},
		// Empty tenant "" is the internal system-chain read: sees ONLY tenant-less
		// system rows, never any tenant's rows (predicate collapses to tenant_id = '').
		{"empty tenant is system-chain read (system rows only)", "", []string{tenantNoneEventID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTenantScopedQuery(t, store, tc.tid, tc.wantIDs)
		})
	}

	// A non-empty non-canonical tenant is rejected at the store (ValidateQueryTenant)
	// — it is never treated as a distinct partition that returns zero rows.
	t.Run("non-canonical tenant is rejected", func(t *testing.T) {
		_, err := store.Query(context.Background(), tenant.TenantID("not-a-uuid"),
			mustRowVisibility(t, tenant.RowScopeTenant, ""),
			ledger.AuditFilters{}, query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
		if err == nil {
			t.Fatal("Query with a non-canonical tenant must return an error, not silently match nothing")
		}
	})
}

// assertTenantScopedQuery runs a tenant.TenantID-partitioned Query and asserts
// the result set is exactly wantIDs (by EventID), so any cross-tenant leakage
// surfaces as a failure. Extracted from runQueryTenantIsolation to keep that
// function's cognitive complexity within budget.
func assertTenantScopedQuery(t *testing.T, store ledger.Store, tenantID string, wantIDs []string) {
	t.Helper()
	want := make(map[string]bool, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = true
	}
	rows, err := store.Query(context.Background(), tenant.TenantID(tenantID), mustRowVisibility(t, tenant.RowScopeTenant, ""),
		ledger.AuditFilters{}, query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(tenant=%q): %v", tenantID, err)
	}
	if len(rows) != len(wantIDs) {
		t.Fatalf("Query(tenant=%q): got %d rows, want %d; got=%v want=%v",
			tenantID, len(rows), len(wantIDs),
			func() []string {
				ids := make([]string, len(rows))
				for i, r := range rows {
					ids[i] = r.EventID
				}
				return ids
			}(),
			wantIDs)
	}
	for _, r := range rows {
		if !want[r.EventID] {
			t.Errorf("Query(tenant=%q): leaked cross-tenant row %q (tenant_id=%q)",
				tenantID, r.EventID, r.TenantID)
		}
	}
}

// appendChainEntry appends one entry under tenantID (for per-tenant chain tests)
// and fatals on error. Factored out of runPerTenantChains to keep its cognitive
// complexity within budget.
func appendChainEntry(t *testing.T, store ledger.Store, eventID, tenantID string, now time.Time) {
	t.Helper()
	e := &ledger.Entry{
		EventID: eventID, EventType: "chain.test", ActorID: "actor",
		TenantID: tenantID, Timestamp: now, Payload: []byte(`{}`),
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf(appendEventErrFmt, eventID, err)
	}
}

// runPerTenantChains verifies the per-(namespace,tenant) chain invariant (#1618):
// each tenant's entries start at SeqNo==1 independently, and cross-tenant
// GetBySeq reads the correct chain without leaking entries between tenants.
func runPerTenantChains(t *testing.T, factory Factory) {
	store, tr, fc, cleanup := factory(t)
	defer cleanup()

	tenantA := tenant.TenantID(conformanceTenantA)
	tenantB := tenant.TenantID(conformanceTenantB)
	vis := mustRowVisibility(t, tenant.RowScopeTenant, "")

	// Interleave two tenants' appends; each chain is independent (per-tenant
	// seq_no), so both reach SeqNo==2 on their own (namespace, tenant) chain.
	appendChainEntry(t, store, chainAEventID, conformanceTenantA, fc.Now())
	appendChainEntry(t, store, chainBEventID, conformanceTenantB, fc.Now())
	appendChainEntry(t, store, "chain-a-2", conformanceTenantA, fc.Now())
	appendChainEntry(t, store, "chain-b-2", conformanceTenantB, fc.Now())

	// Tenant-a chain: Tail shows SeqNo==2.
	tailA, err := scopedTail(t, tr, store, tenantA)
	if err != nil {
		t.Fatalf("Tail(tenant-a): %v", err)
	}
	if tailA.SeqNo != 2 {
		t.Errorf("tenant-a Tail.SeqNo: got %d, want 2 (independent chain)", tailA.SeqNo)
	}
	if tailA.EntryCount != 2 {
		t.Errorf("tenant-a Tail.EntryCount: got %d, want 2", tailA.EntryCount)
	}

	// Tenant-b chain: Tail shows SeqNo==2.
	tailB, err := scopedTail(t, tr, store, tenantB)
	if err != nil {
		t.Fatalf("Tail(tenant-b): %v", err)
	}
	if tailB.SeqNo != 2 {
		t.Errorf("tenant-b Tail.SeqNo: got %d, want 2 (independent chain)", tailB.SeqNo)
	}

	// GetBySeq isolation: tenant-a seq 1 must return chain-a-1, not chain-b-1.
	gotA1, err := scopedGetBySeq(t, tr, store, tenantA, vis, 1)
	if err != nil {
		t.Fatalf("GetBySeq(tenant-a, seq=1): %v", err)
	}
	if gotA1.EventID != chainAEventID {
		t.Errorf("GetBySeq(tenant-a, seq=1): got EventID=%q, want %q", gotA1.EventID, chainAEventID)
	}

	// GetBySeq isolation: tenant-b seq 1 must return chain-b-1, not chain-a-1.
	gotB1, err := scopedGetBySeq(t, tr, store, tenantB, vis, 1)
	if err != nil {
		t.Fatalf("GetBySeq(tenant-b, seq=1): %v", err)
	}
	if gotB1.EventID != chainBEventID {
		t.Errorf("GetBySeq(tenant-b, seq=1): got EventID=%q, want %q", gotB1.EventID, chainBEventID)
	}

	// System chain (unscoped ctx) is separate from both tenant chains.
	sysTail, err := store.Tail(context.Background())
	if err != nil {
		t.Fatalf("Tail(system/unscoped): %v", err)
	}
	if sysTail.EntryCount != 0 {
		t.Errorf("system chain unexpectedly contains %d entries; tenant entries must not bleed into system chain",
			sysTail.EntryCount)
	}
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
	store, tr, fc, cleanup := factory(t)
	defer cleanup()

	e1 := NewEntryFixture(t, "parity-1", "parity.test", "actor-parity", fc.Now())
	if err := store.Append(context.Background(), e1); err != nil {
		t.Fatalf("Append seq 1: %v", err)
	}
	e2 := NewEntryFixture(t, "parity-2", "parity.test", "actor-parity", fc.Now())
	if err := store.Append(context.Background(), e2); err != nil {
		t.Fatalf("Append seq 2: %v", err)
	}

	// NewEntryFixture stamps TenantID="tenant-test"; scope ctx to that chain.
	fixtureTenant := tenant.TenantID(conformanceTenant)
	vis := mustRowVisibility(t, tenant.RowScopeTenant, "")
	got1, err := scopedGetBySeq(t, tr, store, fixtureTenant, vis, 1)
	if err != nil {
		t.Fatalf("GetBySeq 1: %v", err)
	}
	want1 := protocol.ComputeHash("", got1)
	if got1.Hash != want1 {
		t.Errorf("seq 1 hash parity broken: store=%s protocol=%s "+
			"(Store implementation and Protocol must agree byte-for-byte)",
			got1.Hash, want1)
	}

	got2, err := scopedGetBySeq(t, tr, store, fixtureTenant, vis, 2)
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

// runQueryByTraceID verifies that Store.Query correctly filters entries by
// TraceID (contract-fanout: all Store implementations must satisfy this filter
// so conformance is hoisted into Run, not left as a standalone integration test).
//
// Three entries are appended: two share trace T1, one has trace T2. The case
// asserts:
//   - AuditFilters{TraceID:"T1"} returns exactly the two T1 entries.
//   - AuditFilters{TraceID:""}   returns all three (no filter applied).
//   - AuditFilters{TraceID:"no-match"} returns empty.
func runQueryByTraceID(t *testing.T, factory Factory) {
	const (
		traceT1 = "4bf92f3577b34da6a3ce929d0e0e4736"
		traceT2 = "00f067aa0ba902b7000000000000000a"
	)
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	entries := []struct {
		eventID string
		traceID string
	}{
		{"traceid-evt-1", traceT1},
		{"traceid-evt-2", traceT1},
		{"traceid-evt-3", traceT2},
	}
	for _, en := range entries {
		e := &ledger.Entry{
			EventID:   en.eventID,
			EventType: "traceid.conformance",
			ActorID:   "actor",
			TraceID:   en.traceID,
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf(appendEventErrFmt, en.eventID, err)
		}
	}

	// Entries have no TenantID → system chain "". Query with "" sees all chains.
	tid := tenant.TenantID("")
	vis := mustRowVisibility(t, tenant.RowScopeTenant, "")

	// Filter by T1: must return exactly 2 entries.
	byT1, err := store.Query(context.Background(), tid, vis,
		ledger.AuditFilters{TraceID: traceT1},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(TraceID=T1): %v", err)
	}
	if len(byT1) != 2 {
		t.Errorf("Query(TraceID=T1): got %d results, want 2", len(byT1))
	}
	for _, r := range byT1 {
		if r.TraceID != traceT1 {
			t.Errorf("Query(TraceID=T1): got TraceID=%q, want %q", r.TraceID, traceT1)
		}
	}

	// Empty TraceID filter: must return all 3 entries.
	all, err := store.Query(context.Background(), tid, vis,
		ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(empty): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Query(empty TraceID): got %d results, want 3", len(all))
	}

	// Non-matching trace: must return empty.
	noMatch, err := store.Query(context.Background(), tid, vis,
		ledger.AuditFilters{TraceID: "no-match-trace"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("Query(TraceID=no-match): %v", err)
	}
	if len(noMatch) != 0 {
		t.Errorf("Query(TraceID=no-match): got %d results, want 0", len(noMatch))
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

	store, tr, fc, cleanup := factory(t)
	defer cleanup()

	occurredAt := fc.Now().Add(principalOccurredAtSkew).UTC()
	entry := &ledger.Entry{
		EventID:       "principal-roundtrip",
		EventType:     "user.login",
		ActorID:       "actor-impersonator",
		SubjectID:     "subject-end-user",
		TenantID:      conformanceTenantA,
		SessionID:     "sess-42",
		CorrelationID: "corr-xyz-001",
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		OccurredAt:    occurredAt,
		Timestamp:     fc.Now(),
		Payload:       []byte(`{"action":"login"}`),
	}

	if err := store.Append(context.Background(), entry); err != nil {
		t.Fatalf("Append principal-roundtrip: %v", err)
	}

	// Entry has TenantID=conformanceTenantA (a valid UUID, distinct from the
	// default chain — proves the TenantID field roundtrips); scope to that chain.
	got, err := scopedGetBySeq(t, tr, store, tenant.TenantID(conformanceTenantA), mustRowVisibility(t, tenant.RowScopeTenant, ""), 1)
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

// visQueryCase is one row-visibility Query conformance case. It is a named type
// (not an anonymous table) so the per-case assertion lives in the run method
// below, keeping each conformance function's cognitive complexity within budget.
type visQueryCase struct {
	name      string
	scope     tenant.RowScope
	subject   string
	wantCount int
	wantActor string       // non-empty: every result row must carry this actorID
	wantErr   errcode.Code // non-empty: expect this error code; skip count/actor checks
}

// run executes one Query visibility case against store.
// Entries in runQueryVisibilityObligations have no TenantID → system chain "";
// pass "" so Query sees all chains (tenant isolation is tested separately).
func (tc visQueryCase) run(t *testing.T, store ledger.Store, filters ledger.AuditFilters, params query.ListParams) {
	t.Helper()
	vis := mustRowVisibility(t, tc.scope, tc.subject)
	rows, err := store.Query(context.Background(), tenant.TenantID(""), vis, filters, params)
	if tc.wantErr != "" {
		errcodetest.AssertCode(t, err, tc.wantErr)
		if rows != nil {
			t.Errorf("Query(vis=%v) fail-closed: expected nil rows, got %d", tc.scope, len(rows))
		}
		return
	}
	if err != nil {
		t.Fatalf("Query(vis=%v): %v", tc.scope, err)
	}
	if len(rows) != tc.wantCount {
		t.Errorf("Query(vis=%v subject=%q): got %d rows, want %d", tc.scope, tc.subject, len(rows), tc.wantCount)
	}
	for _, r := range rows {
		if tc.wantActor != "" && r.ActorID != tc.wantActor {
			t.Errorf("Query(vis=%v): got ActorID=%q, want %q", tc.scope, r.ActorID, tc.wantActor)
		}
	}
}

// visGetCase is one row-visibility GetBySeq conformance case (named for the same
// cognitive-complexity reason as visQueryCase).
type visGetCase struct {
	name    string
	scope   tenant.RowScope
	subject string
	wantOK  bool         // true = expect the entry; false = expect wantErr
	wantErr errcode.Code // expected error code when wantOK is false
}

// run executes one GetBySeq visibility case against store (seq 1 = the seeded
// alice entry).
func (tc visGetCase) run(t *testing.T, store ledger.Store) {
	t.Helper()
	vis := mustRowVisibility(t, tc.scope, tc.subject)
	got, err := store.GetBySeq(context.Background(), vis, 1)
	if tc.wantOK {
		if err != nil {
			t.Fatalf("GetBySeq(vis=%v subject=%q): got error %v, want entry", tc.scope, tc.subject, err)
		}
		if got.ActorID != "alice" {
			t.Errorf("GetBySeq: got ActorID=%q, want %q", got.ActorID, "alice")
		}
		return
	}
	// Non-OK: must return tc.wantErr (IDOR-safe collapse → ErrAuditLedgerNotFound;
	// RowScopeAll → ErrInternal code with KindNotImplemented → HTTP 501), not the
	// entry. The conformance asserts the Code (ErrInternal, the 5xx wire-collapse
	// code); the 501 status mapping is asserted at the handler layer.
	errcodetest.AssertCode(t, err, tc.wantErr)
	if got != nil {
		t.Errorf("GetBySeq(vis=%v): expected nil entry, got %+v", tc.scope, got)
	}
}

// NOTE: assertErrCode is reserved for non-_NotFound assertions
// (ErrAuditLedgerAlreadyExists, ErrValidationFailed). Any t.Run("..._NotFound", ...)
// table case MUST call errcodetest.AssertCode directly — POSTGRES-NOTFOUND-
// TEST-OTHER-ERROR-MIXUP-ARCHTEST-01 archtest detects funnel calls at the
// _NotFound t.Run body level only; routing through this helper would be a
// cross-function wrapper that escapes detection (godoc-declared blind spot).
//
// runQueryVisibilityObligations verifies that Store.Query correctly applies
// the row-visibility obligation (epic #1337 PR-4) across all four RowScope
// values. Seeds three entries with distinct actorIDs ("alice", "bob", "charlie"),
// then asserts:
//
//   - RowScopeSelf("alice")   → only alice's entries
//   - RowScopeDevice("alice") → same as self (device uses Allows=subject match)
//   - RowScopeTenant("")      → all entries (tenant-wide: no actor filter)
//   - RowScopeAll("")         → fail-closed (RowScopeAllUnsupportedError) on every
//     backend; cross-tenant audit read is deferred to backlog under #1618 FORCE
//     RLS (PR-5 #1343 landed the derivation, not the audit path); no silent
//     degrade to tenant scope.
func runQueryVisibilityObligations(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	actors := []string{"alice", "bob", "charlie"}
	for i, actor := range actors {
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("vis-query-%d", i),
			EventType: "vis.test",
			ActorID:   actor,
			Timestamp: fc.Now(),
			Payload:   []byte(`{}`),
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("Append vis-query-%d: %v", i, err)
		}
	}

	params := query.ListParams{Limit: 50, Sort: ledger.QuerySort()}
	filters := ledger.AuditFilters{}

	cases := []visQueryCase{
		{"self-alice", tenant.RowScopeSelf, "alice", 1, "alice", ""},
		{"device-alice", tenant.RowScopeDevice, "alice", 1, "alice", ""},
		{"tenant-wide", tenant.RowScopeTenant, "", 3, "", ""},
		// RowScopeAll is fail-closed on every backend (deferred to backlog under
		// #1618 FORCE RLS; no silent degrade to tenant scope) — see
		// RowScopeAllUnsupportedError. Code = ErrInternal (5xx wire-collapse);
		// Kind = KindNotImplemented → HTTP 501 at the handler (review F5).
		{"all-fail-closed", tenant.RowScopeAll, "", 0, "", errcode.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, store, filters, params) })
	}
}

// runGetBySeqVisibilityObligations verifies the IDOR-safe collapse contract of
// Store.GetBySeq (epic #1337 PR-4): GetBySeq with a non-matching vis returns
// ErrAuditLedgerNotFound (same as actual not-found), not the entry.
//
// Appends one entry with ActorID="alice". Then asserts:
//   - Self("alice")   → found (owns the entry)
//   - Self("bob")     → ErrAuditLedgerNotFound (IDOR collapse)
//   - Tenant("")      → found (tenant-wide read)
//   - All("")         → fail-closed (RowScopeAllUnsupportedError); deferred backlog
func runGetBySeqVisibilityObligations(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	e := &ledger.Entry{
		EventID:   "vis-getbyseq-1",
		EventType: "vis.getbyseq.test",
		ActorID:   "alice",
		Timestamp: fc.Now(),
		Payload:   []byte(`{}`),
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	cases := []visGetCase{
		{"self-alice-found", tenant.RowScopeSelf, "alice", true, ""},
		{"self-bob-idor-collapse", tenant.RowScopeSelf, "bob", false, errcode.ErrAuditLedgerNotFound},
		{"device-alice-found", tenant.RowScopeDevice, "alice", true, ""},
		{"device-bob-idor-collapse", tenant.RowScopeDevice, "bob", false, errcode.ErrAuditLedgerNotFound},
		{"tenant-wide-found", tenant.RowScopeTenant, "", true, ""},
		// RowScopeAll is fail-closed on every backend (deferred to backlog under
		// #1618 FORCE RLS; distinct from the IDOR-collapse NotFound: it is a
		// deferred CAPABILITY, KindNotImplemented → HTTP 501, Code ErrInternal).
		{"all-fail-closed", tenant.RowScopeAll, "", false, errcode.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, store) })
	}
}

// getByIDMissingID is a syntactically valid UUID the conformance never seeds, so
// every backend's GetByID / GetByIDCrossTenant returns ErrAuditLedgerNotFound for
// it (PG: no row; mem: no matching id). Valid uuid form so the PG `$N::uuid`
// cast / parse-guard treats it as "absent", not "malformed".
const getByIDMissingID = "00000000-0000-0000-0000-000000000000"

// getByIDMalformedID is NOT a valid uuid; both backends must still collapse it to
// ErrAuditLedgerNotFound (PG parse-guards it off the uuid cast → not-found; mem
// finds no matching id), never a 500 / 22P02 leak.
const getByIDMalformedID = "not-a-uuid"

// visGetByIDCase mirrors visGetCase but for GetByID (keyed on the opaque id rather
// than seq_no). The id is captured from the seeded entry's Append write-back, so
// one case table works across mem (id=deterministic hash) and PG (id=uuid) backends.
type visGetByIDCase struct {
	name    string
	scope   tenant.RowScope
	subject string
	wantOK  bool
	wantErr errcode.Code
}

// run executes one GetByID visibility case against store for the captured id of
// the seeded alice entry (tenant=conformanceTenant). Like visGetCase it asserts
// the IDOR-safe collapse / RowScopeAll fail-close, plus the id round-trip on OK.
func (tc visGetByIDCase) run(t *testing.T, store ledger.Store, id string) {
	t.Helper()
	vis := mustRowVisibility(t, tc.scope, tc.subject)
	got, err := store.GetByID(context.Background(), tenant.TenantID(conformanceTenant), vis, id)
	if tc.wantOK {
		if err != nil {
			t.Fatalf("GetByID(vis=%v subject=%q): got error %v, want entry", tc.scope, tc.subject, err)
		}
		if got.ActorID != "alice" {
			t.Errorf("GetByID: got ActorID=%q, want %q", got.ActorID, "alice")
		}
		return
	}
	// Non-OK: must return tc.wantErr (IDOR-safe collapse → ErrAuditLedgerNotFound;
	// RowScopeAll → ErrInternal code / KindNotImplemented → HTTP 501), not the entry.
	errcodetest.AssertCode(t, err, tc.wantErr)
	if got != nil {
		t.Errorf("GetByID(vis=%v): expected nil entry, got %+v", tc.scope, got)
	}
}

// runGetByIDVisibilityObligations mirrors runGetBySeqVisibilityObligations for the
// id-keyed path (#1852): it pins the IDOR-safe owner collapse + the RowScopeAll
// fail-close AND the id ROUND-TRIP — the opaque id the store writes back on Append
// is exactly the id GetByID resolves (the same id http.audit.list.v1 projects and
// http.audit.get.v1 looks up). Seeds one entry (ActorID="alice", tenant=
// conformanceTenant), captures its store-assigned id, then asserts the table.
func runGetByIDVisibilityObligations(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	e := &ledger.Entry{
		EventID:   "vis-getbyid-1",
		EventType: "vis.getbyid.test",
		ActorID:   "alice",
		TenantID:  conformanceTenant,
		Timestamp: fc.Now(),
		Payload:   []byte(`{}`),
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// id round-trip: Append wrote back the opaque store id; GetByID must resolve that
	// exact id (mem id=EventID, PG id=uuid — captured, never hardcoded).
	id := e.ID
	if id == "" {
		t.Fatal("Append did not write back a store id")
	}

	cases := []visGetByIDCase{
		{"self-alice-found", tenant.RowScopeSelf, "alice", true, ""},
		{"self-bob-idor-collapse", tenant.RowScopeSelf, "bob", false, errcode.ErrAuditLedgerNotFound},
		{"device-alice-found", tenant.RowScopeDevice, "alice", true, ""},
		{"device-bob-idor-collapse", tenant.RowScopeDevice, "bob", false, errcode.ErrAuditLedgerNotFound},
		{"tenant-wide-found", tenant.RowScopeTenant, "", true, ""},
		// RowScopeAll is fail-closed on every serving backend (Code ErrInternal,
		// Kind KindNotImplemented → HTTP 501); distinct from the IDOR-collapse 404.
		{"all-fail-closed", tenant.RowScopeAll, "", false, errcode.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, store, id) })
	}
}

// runGetByIDCrossTenantHidden pins the TENANT axis of GetByID: an entry seeded in
// tenant-A is NOT resolvable by a GetByID scoped to tenant-B (the explicit tenant
// predicate excludes it → ErrAuditLedgerNotFound, IDOR-safe), the single-entry
// counterpart of runQueryTenantIsolation.
func runGetByIDCrossTenantHidden(t *testing.T, factory Factory) {
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	e := &ledger.Entry{
		EventID:   "ti-getbyid-a1",
		EventType: "tenant.iso.getbyid",
		ActorID:   "actor",
		TenantID:  isoTenantA,
		Timestamp: fc.Now(),
		Payload:   []byte(`{}`),
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	idA := e.ID

	// tenant-A resolves its own entry by id.
	gotA, err := store.GetByID(context.Background(), tenant.TenantID(isoTenantA),
		mustRowVisibility(t, tenant.RowScopeTenant, ""), idA)
	if err != nil {
		t.Fatalf("GetByID(tenant-A, idA): %v", err)
	}
	if gotA.EventID != "ti-getbyid-a1" {
		t.Errorf("GetByID(tenant-A): got EventID=%q, want ti-getbyid-a1", gotA.EventID)
	}

	// tenant-B must NOT resolve tenant-A's entry by the same id (cross-tenant hidden).
	_, errB := store.GetByID(context.Background(), tenant.TenantID(isoTenantB),
		mustRowVisibility(t, tenant.RowScopeTenant, ""), idA)
	errcodetest.AssertCode(t, errB, errcode.ErrAuditLedgerNotFound)
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

// CrossTenantFactory constructs a fresh CrossTenantQueryStore for conformance
// testing. The factory receives a slice of Entry fixtures to seed (one entry per
// element) so conformance cases can assert cross-tenant multi-store behavior
// without being coupled to a specific backend seeding API.
//
// The cleanup func is called by the conformance helper after each sub-test.
type CrossTenantFactory func(t *testing.T, seed []*ledger.Entry) (store ledger.CrossTenantQueryStore, cleanup func())

// RunCrossTenantQueryConformance runs the cross-tenant query conformance suite
// against the supplied CrossTenantFactory. It covers:
//
//   - Cross-tenant read returns rows from multiple tenants and both namespace
//     chains (the factory must seed entries for at least two distinct tenants).
//   - Pagination: merge-ordered traversal with (timestamp DESC, id ASC) keyset
//     yields every seeded entry exactly once.
//   - AuditFilters narrowing: EventType / ActorID / From / To filters.
//   - Empty sort is rejected (ErrValidationFailed) — same as ordinary Store.Query.
//
// The factory must seed the supplied entries before returning the store. Two
// distinct tenants (crossTenantConformanceTenantA / crossTenantConformanceTenantB)
// and two distinct namespaces (simulated by the factory's MemStore layout) are
// used for correctness.
//
// Note: the ordinary serving Store.Query and GetBySeq paths continue to
// fail-closed for RowScopeAll (RowScopeAllUnsupportedError — defense in depth).
// RunCrossTenantQueryConformance only exercises the CrossTenantQueryStore path,
// not the serving-store path.
func RunCrossTenantQueryConformance(t *testing.T, factory CrossTenantFactory) {
	t.Helper()

	t.Run("CrossTenant_MultiTenant_MultiNamespace", func(t *testing.T) {
		runCTMultiTenantMultiNamespace(t, factory)
	})
	t.Run("CrossTenant_Pagination", func(t *testing.T) {
		runCTPagination(t, factory)
	})
	t.Run("CrossTenant_Filter_EventType", func(t *testing.T) {
		runCTFilterEventType(t, factory)
	})
	t.Run("CrossTenant_Filter_ActorID", func(t *testing.T) {
		runCTFilterActorID(t, factory)
	})
	t.Run("CrossTenant_Filter_TimeRange", func(t *testing.T) {
		runCTFilterTimeRange(t, factory)
	})
	t.Run("CrossTenant_EmptySort_Rejected", func(t *testing.T) {
		runCTEmptySortRejected(t, factory)
	})
	t.Run("CrossTenant_ZeroObligation_Rejected", func(t *testing.T) {
		runCTZeroObligationRejected(t, factory)
	})
	t.Run("CrossTenant_Filter_SubjectID", func(t *testing.T) {
		runCTFilterSubjectID(t, factory)
	})
	t.Run("CrossTenant_Filter_TraceID", func(t *testing.T) {
		runCTFilterTraceID(t, factory)
	})
	t.Run("CrossTenant_InvalidCharFilter_Rejected", func(t *testing.T) {
		runCTInvalidCharFilterRejected(t, factory)
	})
	t.Run("CrossTenant_GetByID_Found", func(t *testing.T) {
		runCTGetByIDFound(t, factory)
	})
	t.Run("CrossTenant_GetByID_NotFound", func(t *testing.T) {
		runCTGetByIDNotFound(t, factory)
	})
	t.Run("CrossTenant_GetByID_DuplicateEventID_GloballyUniqueId", func(t *testing.T) {
		runCTGetByIDDuplicateEventID(t, factory)
	})
}

// runCTGetByIDDuplicateEventID pins F1 (#2288 review): the public id projected by a
// cross-tenant read MUST be globally unique, so GetByIDCrossTenant never returns the
// WRONG tenant's row when two tenants reuse the same EventID. EventID is unique only
// per (namespace, tenant) (uq_audit_ns_tenant_event_id), so tenant A and tenant B may
// legitimately share one — a backend that set the public id to the bare EventID (the
// old MemStore behavior) would return whichever row map iteration hit first. Seeds the
// same EventID under two tenants; asserts QueryCrossTenant returns two rows with
// DISTINCT ids, and GetByIDCrossTenant resolves the RIGHT tenant's row for each id.
func runCTGetByIDDuplicateEventID(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	const dupEventID = "ct-dup-evt"
	base := epochAnchor
	seed := []*ledger.Entry{
		{
			EventID: dupEventID, EventType: "ct.dup", ActorID: "actor-a",
			TenantID: crossTenantConformanceTenantA, Timestamp: base.Add(time.Millisecond), Payload: []byte(`{}`),
		},
		{
			EventID: dupEventID, EventType: "ct.dup", ActorID: "actor-b",
			TenantID: crossTenantConformanceTenantB, Timestamp: base.Add(ctTs2), Payload: []byte(`{}`),
		},
	}
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("QueryCrossTenant: got %d rows, want 2 (both tenants' same-EventID entries)", len(rows))
	}
	if rows[0].ID == rows[1].ID {
		t.Fatalf("cross-tenant ids must be globally unique even when EventID is shared; both = %q", rows[0].ID)
	}
	for _, want := range rows {
		got, gerr := store.GetByIDCrossTenant(context.Background(), ctv, want.ID)
		if gerr != nil {
			t.Fatalf("GetByIDCrossTenant(%q): %v", want.ID, gerr)
		}
		if got.TenantID != want.TenantID || got.ActorID != want.ActorID {
			t.Errorf("GetByIDCrossTenant(%q): got tenant=%q actor=%q, want tenant=%q actor=%q "+
				"(wrong-tenant row — public id not globally unique)",
				want.ID, got.TenantID, got.ActorID, want.TenantID, want.ActorID)
		}
	}
}

// runCTGetByIDFound: GetByIDCrossTenant resolves a seeded entry by its store id
// across tenants (the single-entry counterpart of runCTMultiTenantMultiNamespace).
// The id is discovered from a QueryCrossTenant result so the test is independent of
// the backend's id-assignment scheme (mem EventID vs PG uuid).
func runCTGetByIDFound(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	store, cleanup := factory(t, ctSeed(epochAnchor))
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil || len(rows) == 0 {
		t.Fatalf("seed query: err=%v rows=%d", err, len(rows))
	}
	want := rows[0]
	got, err := store.GetByIDCrossTenant(context.Background(), ctv, want.ID)
	if err != nil {
		t.Fatalf("GetByIDCrossTenant(%q): %v", want.ID, err)
	}
	if got.EventID != want.EventID || got.TenantID != want.TenantID {
		t.Errorf("GetByIDCrossTenant: got (EventID=%q,TenantID=%q), want (%q,%q)",
			got.EventID, got.TenantID, want.EventID, want.TenantID)
	}
}

// runCTGetByIDNotFound: GetByIDCrossTenant for an unseeded id returns
// ErrAuditLedgerNotFound on every cross-tenant backend.
func runCTGetByIDNotFound(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	store, cleanup := factory(t, ctSeed(epochAnchor))
	defer cleanup()
	_, err := store.GetByIDCrossTenant(context.Background(),
		tenant.NewCrossTenantVisibility(), getByIDMissingID)
	errcodetest.AssertCode(t, err, errcode.ErrAuditLedgerNotFound)
}

// conformance tenant UUIDs for cross-tenant tests (distinct from the existing
// per-tenant-chain isolation UUIDs to avoid fixture collisions).
const (
	crossTenantConformanceTenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	crossTenantConformanceTenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// Per-entry timestamp offsets for the cross-tenant seeds + the From/To filter
// bound (TEST-TIME-LITERAL-01: no inline N*time.Millisecond literals).
const (
	ctTs2 = 2 * time.Millisecond
	ctTs3 = 3 * time.Millisecond
	ctTs4 = 4 * time.Millisecond
	ctTs5 = 5 * time.Millisecond
)

// ctSeed returns a canonical cross-tenant seed: 3 entries for tenant-A
// (timestamps T1, T3, T5) and 2 for tenant-B (T2, T4), seeded across the two
// "namespace chains" the factory represents. The ordering mixes tenants so
// merge-sort correctness is observable.
func ctSeed(base time.Time) []*ledger.Entry {
	mk := func(eventID, tenantID, actorID, eventType string, ts time.Time) *ledger.Entry {
		return &ledger.Entry{
			EventID:   eventID,
			EventType: eventType,
			ActorID:   actorID,
			TenantID:  tenantID,
			Timestamp: ts,
			Payload:   []byte(`{}`),
		}
	}
	return []*ledger.Entry{
		mk("ct-a1", crossTenantConformanceTenantA, "actor-alice", "ct.test", base.Add(time.Millisecond)),
		mk("ct-b1", crossTenantConformanceTenantB, "actor-bob", "ct.test", base.Add(ctTs2)),
		mk("ct-a2", crossTenantConformanceTenantA, "actor-alice", "ct.test", base.Add(ctTs3)),
		mk("ct-b2", crossTenantConformanceTenantB, "actor-bob", "ct.other", base.Add(ctTs4)),
		mk("ct-a3", crossTenantConformanceTenantA, "actor-charlie", "ct.other", base.Add(ctTs5)),
	}
}

// runCTMultiTenantMultiNamespace asserts that QueryCrossTenant returns entries
// from BOTH tenants and all namespace chains with no tenant leakage or omission.
func runCTMultiTenantMultiNamespace(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctSeed(base)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant: %v", err)
	}

	// All 5 seed entries must be visible.
	if len(rows) != len(seed) {
		t.Fatalf("QueryCrossTenant: got %d rows, want %d", len(rows), len(seed))
	}

	// Both tenants must appear in the result.
	tenantsSeen := make(map[string]bool)
	for _, r := range rows {
		tenantsSeen[r.TenantID] = true
	}
	for _, wantTenant := range []string{crossTenantConformanceTenantA, crossTenantConformanceTenantB} {
		if !tenantsSeen[wantTenant] {
			t.Errorf("QueryCrossTenant: tenant %q not found in results", wantTenant)
		}
	}

	// Results must be in timestamp DESC order.
	for i := 1; i < len(rows); i++ {
		if rows[i].Timestamp.After(rows[i-1].Timestamp) {
			t.Errorf("QueryCrossTenant: ordering violation at index %d: %v > %v",
				i, rows[i].Timestamp, rows[i-1].Timestamp)
		}
	}
}

// runCTPagination verifies that QueryCrossTenant with a small page limit
// traverses all entries exactly once via the (timestamp DESC, id ASC) keyset.
func runCTPagination(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctSeed(base)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	var collected []string
	var cursorVals []any
	const pageSize = 2
	const maxIter = 10

	for iter := 0; iter < maxIter; iter++ {
		rows, err := store.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
			query.ListParams{Limit: pageSize, Sort: ledger.QuerySort(), CursorValues: cursorVals})
		if err != nil {
			t.Fatalf("QueryCrossTenant iter %d: %v", iter, err)
		}
		hasMore := len(rows) > pageSize
		page := rows
		if hasMore {
			page = rows[:pageSize]
		}
		for _, e := range page {
			collected = append(collected, e.EventID)
		}
		if !hasMore {
			break
		}
		last := page[len(page)-1]
		cursorVals = []any{last.Timestamp.Format(time.RFC3339Nano), last.ID}
	}

	if len(collected) != len(seed) {
		t.Fatalf("Pagination: traversed %d entries, want %d; got=%v",
			len(collected), len(seed), collected)
	}
	// No duplicates.
	seen := make(map[string]bool, len(collected))
	for _, id := range collected {
		if seen[id] {
			t.Errorf("Pagination: duplicate EventID %q in traversal", id)
		}
		seen[id] = true
	}
}

// runCTFilterEventType verifies AuditFilters.EventType narrows the cross-tenant result.
func runCTFilterEventType(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctSeed(base) // 3 "ct.test" + 2 "ct.other"
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{EventType: "ct.test"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(EventType=ct.test): %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("EventType filter: got %d, want 3", len(rows))
	}
	for _, r := range rows {
		if r.EventType != "ct.test" {
			t.Errorf("EventType filter leaked %q", r.EventType)
		}
	}
}

// runCTFilterActorID verifies AuditFilters.ActorID narrows the cross-tenant result
// across tenants. actor-alice has 2 entries (both in tenant-A); actor-bob 2 (both
// tenant-B), actor-charlie 1 (tenant-A).
func runCTFilterActorID(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctSeed(base)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{ActorID: "actor-alice"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(ActorID=actor-alice): %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ActorID filter: got %d, want 2 (alice entries)", len(rows))
	}
	for _, r := range rows {
		if r.ActorID != "actor-alice" {
			t.Errorf("ActorID filter leaked actor %q", r.ActorID)
		}
	}
}

// runCTFilterTimeRange verifies AuditFilters.From / To narrowing.
func runCTFilterTimeRange(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctSeed(base)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	// From=T2, To=T4 → should include entries at T2, T3, T4 (3 entries).
	rows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{
			From: base.Add(ctTs2),
			To:   base.Add(ctTs4),
		},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(From/To): %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("TimeRange filter: got %d, want 3 (T2..T4)", len(rows))
	}
}

// runCTEmptySortRejected verifies that QueryCrossTenant rejects an empty Sort
// with ErrValidationFailed — consistent with Store.Query and MultiStore.Query.
func runCTEmptySortRejected(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	store, cleanup := factory(t, nil)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	_, err := store.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 10})
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// runCTZeroObligationRejected pins the F2 data-layer PEP: every
// CrossTenantQueryStore implementation MUST fail-closed when handed a zero/invalid
// CrossTenantVisibility. Go's zero value (tenant.CrossTenantVisibility{}) is
// constructable despite the sealed minter, so a store that skipped obligation
// validation would happily return the seeded rows — seeding real entries makes the
// rejection non-vacuous (anti-vacuity). A valid Sort is passed so the rejection is
// the obligation check (KindInternal/ErrInternal), not the empty-sort guard. This
// is the single-source machine-checked contract that holds for mem, PG, and any
// future backend wired into RunCrossTenantQueryConformance.
func runCTZeroObligationRejected(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	seed := ctSeed(epochAnchor)
	store, cleanup := factory(t, seed)
	defer cleanup()

	var zero tenant.CrossTenantVisibility // invalid obligation (scope=0)
	rows, err := store.QueryCrossTenant(context.Background(), zero, ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	errcodetest.AssertCode(t, err, errcode.ErrInternal)
	if len(rows) != 0 {
		t.Errorf("zero-obligation cross-tenant read returned %d rows; must fail-closed with none", len(rows))
	}

	// The single-entry path (GetByIDCrossTenant, #1852) enforces the SAME F2 PEP.
	// Discover a REAL seeded id under a valid obligation, then prove the ZERO
	// obligation still fail-closes for that real id (anti-vacuity: the rejection is
	// the obligation check, KindInternal/ErrInternal, NOT a not-found — a store that
	// skipped ctv.Validate would return the real entry).
	valid := tenant.NewCrossTenantVisibility()
	realRows, qErr := store.QueryCrossTenant(context.Background(), valid, ledger.AuditFilters{},
		query.ListParams{Limit: 1, Sort: ledger.QuerySort()})
	if qErr != nil || len(realRows) == 0 {
		t.Fatalf("seed query under valid obligation: err=%v rows=%d", qErr, len(realRows))
	}
	gotEntry, idErr := store.GetByIDCrossTenant(context.Background(), zero, realRows[0].ID)
	errcodetest.AssertCode(t, idErr, errcode.ErrInternal)
	if gotEntry != nil {
		t.Errorf("zero-obligation cross-tenant get-by-id returned a real entry; must fail-closed")
	}
}

// ctFilterSeed returns a seed with ≥2 distinct SubjectIDs and ≥2 distinct
// TraceIDs across two tenants, so SubjectID/TraceID filter conformance tests
// can assert the correct row counts without relying on the shared ctSeed (which
// has identical SubjectIDs and no TraceIDs).
//
// Seed layout:
//
//	ct-f1: tenantA, subject=subject-alice, trace=trace-X, event=ct.filter
//	ct-f2: tenantA, subject=subject-alice, trace=trace-Y, event=ct.filter
//	ct-f3: tenantB, subject=subject-bob,   trace=trace-X, event=ct.filter
//	ct-f4: tenantB, subject=subject-bob,   trace=trace-Z, event=ct.other
func ctFilterSeed(base time.Time) []*ledger.Entry {
	mk := func(eventID, tenantID, subjectID, traceID, eventType string, ts time.Time) *ledger.Entry {
		return &ledger.Entry{
			EventID:   eventID,
			EventType: eventType,
			ActorID:   "actor-filter",
			SubjectID: subjectID,
			TenantID:  tenantID,
			TraceID:   traceID,
			Timestamp: ts,
			Payload:   []byte(`{}`),
		}
	}
	return []*ledger.Entry{
		mk("ct-f1", crossTenantConformanceTenantA, "subject-alice", "trace-X", "ct.filter", base.Add(time.Millisecond)),
		mk("ct-f2", crossTenantConformanceTenantA, "subject-alice", "trace-Y", "ct.filter", base.Add(ctTs2)),
		mk("ct-f3", crossTenantConformanceTenantB, "subject-bob", "trace-X", "ct.filter", base.Add(ctTs3)),
		mk("ct-f4", crossTenantConformanceTenantB, "subject-bob", "trace-Z", "ct.other", base.Add(ctTs4)),
	}
}

// runCTFilterSubjectID verifies AuditFilters.SubjectID narrows the cross-tenant
// result to only entries whose SubjectID matches, across both tenants.
//
// ctFilterSeed has 2 entries with SubjectID="subject-alice" (tenantA) and 2 with
// "subject-bob" (tenantB). Filtering by "subject-alice" must return exactly 2.
//
//nolint:dupl // intentionally mirrors runCTFilterTraceID: conformance functions share structure
func runCTFilterSubjectID(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctFilterSeed(base)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{SubjectID: "subject-alice"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(SubjectID=subject-alice): %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("SubjectID filter: got %d rows, want 2 (alice has 2 entries across tenants)", len(rows))
	}
	for _, r := range rows {
		if r.SubjectID != "subject-alice" {
			t.Errorf("SubjectID filter leaked row with SubjectID=%q", r.SubjectID)
		}
	}

	// Sanity: "subject-bob" yields 2 entries.
	bobRows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{SubjectID: "subject-bob"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(SubjectID=subject-bob): %v", err)
	}
	if len(bobRows) != 2 {
		t.Errorf("SubjectID filter: got %d rows for subject-bob, want 2", len(bobRows))
	}
}

// runCTFilterTraceID verifies AuditFilters.TraceID narrows the cross-tenant
// result to only entries whose TraceID matches, across both tenants.
//
// ctFilterSeed has 2 entries with TraceID="trace-X" (one per tenant), 1 with
// "trace-Y" (tenantA), and 1 with "trace-Z" (tenantB). Filtering by "trace-X"
// must return exactly 2.
//
//nolint:dupl // intentionally mirrors runCTFilterSubjectID: conformance functions share structure
func runCTFilterTraceID(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	base := epochAnchor
	seed := ctFilterSeed(base)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{TraceID: "trace-X"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(TraceID=trace-X): %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("TraceID filter: got %d rows, want 2 (trace-X appears in both tenants)", len(rows))
	}
	for _, r := range rows {
		if r.TraceID != "trace-X" {
			t.Errorf("TraceID filter leaked row with TraceID=%q", r.TraceID)
		}
	}

	// Sanity: "trace-Y" yields exactly 1 entry.
	traceYRows, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{TraceID: "trace-Y"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(TraceID=trace-Y): %v", err)
	}
	if len(traceYRows) != 1 {
		t.Errorf("TraceID filter: got %d rows for trace-Y, want 1", len(traceYRows))
	}
}

// runQueryInvalidCharFilterRejected asserts that Store.Query rejects an
// AuditFilters with an unsafe char in ActorID (ValidateQueryFilters defense-in-depth,
// #1742 / #2199). This is a cross-backend conformance case: every backend that
// adds a Store.Query implementation must call ValidateQueryFilters or this test
// catches the omission. At least one entry is seeded so MemStore's path actually
// reaches the validation gate.
func runQueryInvalidCharFilterRejected(t *testing.T, factory Factory) {
	t.Helper()
	store, _, fc, cleanup := factory(t)
	defer cleanup()

	// Seed one entry so the store is non-empty (anti-vacuity: rejection must come
	// from ValidateQueryFilters, not from "zero rows returned").
	e := NewEntryFixture(t, "invalid-filter-evt", "filter.test", "actor", fc.Now())
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// '@' is outside the SafeID charset — ValidateQueryFilters must reject it.
	_, err := store.Query(context.Background(), tenant.TenantID(""),
		mustRowVisibility(t, tenant.RowScopeTenant, ""),
		ledger.AuditFilters{ActorID: "actor@injection"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// runCTInvalidCharFilterRejected asserts that CrossTenantQueryStore.QueryCrossTenant
// rejects an AuditFilters with an unsafe char in ActorID (ValidateQueryFilters
// defense-in-depth, F1 of #2199 review). Seeds real entries so the rejection is
// non-vacuous (the store must actively reject, not merely return zero rows).
func runCTInvalidCharFilterRejected(t *testing.T, factory CrossTenantFactory) {
	t.Helper()
	seed := ctSeed(epochAnchor) // seed real entries (anti-vacuity)
	store, cleanup := factory(t, seed)
	defer cleanup()

	ctv := tenant.NewCrossTenantVisibility()
	// '@' is outside the SafeID charset — ValidateQueryFilters must reject it.
	_, err := store.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{ActorID: "actor@injection"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}
