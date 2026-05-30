package storetest

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// namespaceMaxLen mirrors the unexported ledger.maxNamespaceIDLen. Like
// referenceHashInput mirrors auditHashInput, this independent copy lets the
// fuzz pin the documented contract without importing the production constant;
// drift surfaces as a fuzz failure here.
const namespaceMaxLen = 48

// namespaceLegalSet reports whether s is in the documented NamespaceID legal
// set: non-empty, length ≤ 48, every byte in [a-z_]. This is the contract the
// fuzz holds ledger.NamespaceID.Validate to — independent of Validate's own
// implementation.
func namespaceLegalSet(s string) bool {
	if s == "" || len(s) > namespaceMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// FuzzNamespaceID exercises the full NamespaceID legal set ([a-z_], ≤48) on two
// orthogonal axes, both pure (no store, so the namespace can be enumerated
// freely):
//
//   - Validation contract: ParseNamespaceID accepts s iff namespaceLegalSet(s),
//     and an accepted value round-trips string(ns) == s. This pins the tightened
//     [a-z_]-only contract (issue #1249) and guards against over- or
//     under-rejection.
//   - HMAC domain separation: for every accepted namespace, the production
//     Protocol.ComputeHash agrees byte-for-byte with the independent
//     referenceComputeHash mirror over the same entry. The namespace is the
//     first signed field (ADR-1042 §A); since both MemStore and PG persist
//     Protocol.ComputeHash output (proven by Run's Protocol_HashParity /
//     PrincipalFields_RoundTrip on both backends), this transitively guarantees
//     cross-store HMAC parity across the whole namespace set.
func FuzzNamespaceID(f *testing.F) {
	// Legal seeds.
	for _, s := range []string{"auditcore", "bootstrap", "_runtime", "a", strings.Repeat("a", namespaceMaxLen)} {
		f.Add(s)
	}
	// Illegal seeds (each violates exactly one rule).
	illegal := []string{
		"", "AuditCore", "audit:core", "audit{core", "audit}core",
		"audit-core", "audit1core", "a.b", "-audit",
		strings.Repeat("a", namespaceMaxLen+1),
	}
	for _, s := range illegal {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		ns, err := ledger.ParseNamespaceID(s)
		legal := namespaceLegalSet(s)

		if (err == nil) != legal {
			t.Fatalf("ParseNamespaceID(%q): accepted=%v, want legal=%v", s, err == nil, legal)
		}
		if err != nil {
			return
		}
		if string(ns) != s {
			t.Fatalf("ParseNamespaceID(%q): round-trip changed value to %q", s, string(ns))
		}

		// HMAC namespace domain separation: ComputeHash must match the
		// independent reference for this namespace.
		key := TestHMACKey()
		p, perr := ledger.NewProtocol(
			ns, TestHMACKey(),
			ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
			ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
		)
		if perr != nil {
			t.Fatalf("NewProtocol(ns=%q): %v", s, perr)
		}
		e := NewEntryFixture(t, "ns-fuzz", "", "", epochAnchor)
		got := p.ComputeHash("", e)
		want := referenceComputeHash(key, ns, "", e)
		if got != want {
			t.Errorf("namespace %q HMAC parity broken:\n  ComputeHash=%s\n  reference  =%s", s, got, want)
		}
	})
}
