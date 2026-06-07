package storetest

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// fuzzTimePrecision is the timestamp granularity the round-trip fuzz normalises
// to. PostgreSQL timestamptz stores microseconds; nanosecond-granular fuzzed
// timestamps would survive on MemStore but truncate on PG, breaking both the
// byte-for-byte field round-trip (time.Equal) and the HMAC parity (the digest
// signs *UnixNano). Truncating fuzz inputs to microseconds keeps the input
// inside the precision both backends can represent identically.
const fuzzTimePrecision = time.Microsecond

// RunEntryRoundTripFuzz is the property-based complement to the example-based
// Run suite: it generates random 12-field [ledger.Entry] values plus a binary
// payload corpus and asserts, for every accepted Append, that
//
//   - every caller-supplied field round-trips byte-for-byte through
//     Append → GetBySeq (via AssertEntryRoundTrip, reflect-driven so new Entry
//     fields are covered automatically), and
//   - the store-persisted Hash equals an INDEPENDENT canonical HMAC recomputed
//     by ReferenceComputeHash (the external mirror, never Protocol.ComputeHash)
//     over an independently reconstructed prev_hash, so a silently dropped
//     field in ComputeHash or a mis-linked chain entry is caught.
//
// store and protocol MUST be same-source: protocol must be the very protocol
// the store was constructed with, and it must use TestHMACKey() as its HMAC key
// (i.e. built via NewTestProtocol) — the parity assertion recomputes the
// reference digest with TestHMACKey() and protocol.Namespace(), so a mismatched
// key or namespace would surface as a spurious parity failure, not a store bug.
//
// store and protocol are constructed once by the caller and reused across all
// fuzz iterations — PG cannot afford a fresh migrated database per iteration.
// The chain therefore grows unbounded over a long run (every accepted Append
// adds an entry); bound it with -fuzztime rather than expecting per-iteration
// reset. The same exported helper is wired into both the MemStore fuzz
// (suite_test.go) and the PG fuzz (adapters/postgres, integration tag) with a
// shared seed corpus, so mem-vs-PG round-trip + HMAC parity is exercised in
// fuzz form: both backends must satisfy the same independent reference.
//
// The HMAC canonical-input invariant itself is statically frozen Hard by
// archtest AUDIT-HASH-INPUT-FROZEN-01 (sealed auditHashInput + locked hmac.New
// callsite); this fuzz adds the runtime behavior that a static archtest cannot
// express — µs precision handling, payload binary safety, and namespace domain
// separation under arbitrary inputs.
// RunEntryRoundTripFuzz accepts a txRunner so that PG-backed fuzz tests can
// wrap scoped GetBySeq calls inside RunInTx (required by PG pool deep-defense).
// Pass storetest.PassthroughTxRunner() for MemStore targets.
func RunEntryRoundTripFuzz(f *testing.F, store ledger.Store, protocol *ledger.Protocol, txRunner persistence.TxRunner) {
	if store == nil {
		f.Fatal("storetest.RunEntryRoundTripFuzz: store must not be nil")
	}
	if protocol == nil {
		f.Fatal("storetest.RunEntryRoundTripFuzz: protocol must not be nil")
	}
	if txRunner == nil {
		f.Fatal("storetest.RunEntryRoundTripFuzz: txRunner must not be nil")
	}
	seedEntryRoundTripCorpus(f)

	f.Fuzz(func(t *testing.T,
		eventID, eventType, actorID, subjectID, sessionID, tenantID, correlationID, traceID string,
		occNano, tsNano int64, payload []byte,
	) {
		// Restrict string fields to the domain both backends can store
		// identically: PostgreSQL text columns reject NUL (U+0000) and
		// non-UTF-8 bytes, while a Go string (MemStore) accepts them. Inputs
		// outside that shared domain are not a store bug — skip them so the
		// parity assertion only fires on values both stores can represent.
		// Arbitrary binary content is exercised through the BYTEA Payload
		// (within valid-JSON-object framing), not the text identity fields.
		// traceID is also a text column so it must satisfy the same constraint.
		for _, s := range []string{eventID, eventType, actorID, subjectID, sessionID, tenantID, correlationID, traceID} {
			if !pgRepresentableString(s) {
				return
			}
		}

		src := &ledger.Entry{
			EventID:       eventID,
			EventType:     eventType,
			ActorID:       actorID,
			SubjectID:     subjectID,
			SessionID:     sessionID,
			TenantID:      tenantID,
			CorrelationID: correlationID,
			TraceID:       traceID,
			OccurredAt:    time.Unix(0, occNano).UTC().Truncate(fuzzTimePrecision),
			Timestamp:     time.Unix(0, tsNano).UTC().Truncate(fuzzTimePrecision),
			Payload:       payload,
		}
		assertEntryRoundTripParity(t, store, protocol, txRunner, src)
	})
}

// assertEntryRoundTripParity appends src, and on a successful (non-rejected)
// Append asserts the persisted entry round-trips byte-for-byte and its Hash
// matches the independent reference HMAC.
//
// Only ErrValidationFailed (payload is not a JSON object/null) and
// ErrAuditLedgerAlreadyExists (duplicate EventID fingerprint) are legitimate
// protocol rejections to skip. Every other error — any other errcode, or a
// non-errcode error such as a PG infrastructure failure — is a t.Fatalf, so an
// infra fault can never be silently mistaken for "covered".
func assertEntryRoundTripParity(t *testing.T, store ledger.Store, protocol *ledger.Protocol, tr persistence.TxRunner, src *ledger.Entry) {
	t.Helper()

	if err := store.Append(context.Background(), src); err != nil {
		var ec *errcode.Error
		if errors.As(err, &ec) &&
			(ec.Code == errcode.ErrValidationFailed || ec.Code == errcode.ErrAuditLedgerAlreadyExists) {
			return
		}
		t.Fatalf("Append: %v", err)
	}

	// Append writes the assigned SeqNo back onto src (both MemStore and the PG
	// store do this), so the read-back targets the exact sequence number this
	// call produced — no reliance on Tail pointing at our entry, hence no
	// assumption about iteration ordering. AssertEntryRoundTrip skips the
	// store-assigned fields (SeqNo/ID/PrevHash/Hash), so populating src here is
	// harmless to the field comparison.
	//
	// GetBySeq derives the tenant chain from ctx via tenantScopeOrSystem; scope
	// to src.TenantID so reads target the same per-tenant chain Append wrote to.
	// scopedGetBySeq wraps the call in RunInTx so PG's pool deep-defense doesn't
	// fire when a scope-bearing ctx is used outside a transaction.
	fuzzVis := mustRowVisibility(t, tenant.RowScopeTenant, "")
	fuzzTenant := tenant.TenantID(src.TenantID)
	got, err := scopedGetBySeq(t, tr, store, fuzzTenant, fuzzVis, src.SeqNo)
	if err != nil {
		t.Fatalf("GetBySeq(%d): %v", src.SeqNo, err)
	}

	AssertEntryRoundTrip(t, src, got)

	// Reconstruct the chain link independently rather than trusting the store's
	// own got.PrevHash: the previous entry's persisted Hash (already parity-
	// verified in an earlier iteration) is the canonical prev_hash. Feeding the
	// store's got.PrevHash back into the reference would let a store that links
	// to the wrong predecessor stay self-consistent and pass. seq==1 is the
	// chain root (prev_hash = "").
	prevHash := ""
	if src.SeqNo > 1 {
		prev, perr := scopedGetBySeq(t, tr, store, fuzzTenant, fuzzVis, src.SeqNo-1)
		if perr != nil {
			t.Fatalf("GetBySeq(%d) for chain link: %v", src.SeqNo-1, perr)
		}
		prevHash = prev.Hash
	}

	want := ReferenceComputeHash(t, TestHMACKey(), protocol.Namespace(), prevHash, got)
	if got.Hash != want {
		t.Errorf("HMAC parity broken under fuzz:\n  store=%s\n  ref  =%s", got.Hash, want)
	}
}

// pgRepresentableString reports whether s can be stored verbatim in a
// PostgreSQL text column: valid UTF-8 with no NUL byte. MemStore accepts any
// Go string, so this is the narrower of the two backends and defines the
// shared parity domain for the identity fields.
func pgRepresentableString(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return false
		}
	}
	return true
}

// seedEntryRoundTripCorpus adds the issue #1249 payload corpus and a few field
// permutations as fuzz seeds. Timestamps derive from the suite epochAnchor +
// principalOccurredAtSkew (no new time literals; TEST-TIME-LITERAL-01). Every
// payload is a valid JSON object/null so the seed survives validatePayloadJSON;
// the interesting binary content (pipe byte, Unicode boundary, escaped NUL,
// non-alphabetical multi-key) lives inside JSON string values, which is the
// only shape a strict-JSON-object payload can carry.
//
// traceID is the new 8th string parameter added to the fuzz signature for
// Entry.TraceID coverage; seeds use empty, a canonical 32-hex W3C trace-id,
// and a second distinct trace to exercise TraceID round-trip across backends.
func seedEntryRoundTripCorpus(f *testing.F) {
	tsNano := epochAnchor.UnixNano()
	occNano := epochAnchor.Add(principalOccurredAtSkew).UnixNano()

	add := func(eventID, traceID, payload string) {
		f.Add(
			eventID, "audit.test", "actor-1", "subject-1", "session-1", "tenant-1", "corr-1", traceID,
			occNano, tsNano, []byte(payload),
		)
	}

	// traceID seeds: empty (no trace), a canonical 32-hex W3C trace-id, and a
	// second distinct trace to exercise filtering correctness across backends.
	add("seed-empty-object", "", `{}`)
	add("seed-null", "4bf92f3577b34da6a3ce929d0e0e4736", `null`)
	add("seed-empty-bytes", "", ``)
	add("seed-pipe-byte", "", `{"k":"a|b|c"}`) // pipe byte (legacy HMAC delimiter)
	add("seed-unicode", "", `{"k":"日本語"}`)     // multi-byte UTF-8 boundary
	add("seed-emoji", "", `{"k":"😀🔒"}`)        // surrogate-range / 4-byte runes
	// JSON unicode-escape for NUL (6 ASCII chars in JSON payload, valid JSON);
	// uses a regular string literal so the source file contains no raw NUL byte.
	add("seed-escaped-nul", "", "{\"k\":\"a\\u0000b\"}")
	add("seed-multikey", "", `{"b":1,"a":2,"c":"x"}`) // non-alphabetical multi-key (A-01 guard)
	add("seed-nested", "00f067aa0ba902b7000000000000000a", `{"x":{"y":[1,2,3]},"z":true}`)

	// Field-permutation seeds: distinct values across every identity field so
	// the fuzzer starts from a corpus that would surface a field-swap bug
	// (e.g. SubjectID/TenantID crossed) through AssertEntryRoundTrip.
	f.Add(
		"seed-perm-a", "user.login", "impersonator", "end-user", "sess-42", "tenant-alpha", "corr-xyz",
		"4bf92f3577b34da6a3ce929d0e0e4736",
		occNano, tsNano, []byte(`{"action":"login"}`),
	)
	f.Add(
		"e", "t", "a", "s", "se", "te", "c", "",
		occNano, tsNano, []byte(`{}`),
	)
}
