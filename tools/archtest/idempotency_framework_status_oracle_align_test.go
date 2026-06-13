//go:build archtest

// INVARIANT: IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01
//
// The kernel-side literal metadata.FrameworkIdempotencyStatuses() (hand-written,
// because kernel/ must not import runtime/) MUST equal the single runtime source
// runtime/http/idempotency.FrameworkStatuses(), which derives its set from the
// actual sentinels the idempotency middleware emits (errInProgress → 409 ClaimBusy;
// ErrFingerprintMismatch → 422 key-reused). The auth-shape-aware oracle
// metadata.HTTPTransportMeta.IdempotencyFrameworkStatuses() returns that literal for
// a PrincipalUser-reachable mutating route, so it is checked too.
//
// Why this exists: the kernel literal is the SOLE source of the idempotency
// framework statuses (compute-only, #1591) — the statuses are never hand-authored
// per contract; CH-07 (kernel/governance) forbids them in auth.responses, and
// declaredErrorStatuses folds the oracle into the contract surface. The literal can
// silently outgrow the middleware's runtime behavior (e.g. a new injected status, or
// the 422 upgrade reverted). Binding the literal to the runtime source closes that
// drift: change the middleware-emitted set without updating the kernel literal (or
// vice versa) and this test fails.
//
// AI-robust rating: Medium. A type-system Hard binding (the kernel literal
// derived from the runtime source at compile time) is unreachable — the layering
// rule forbids kernel/ importing runtime/ (the oracle is consumed by kernel/
// governance), so the kernel side must re-declare the set as a literal. This
// archtest — importing both packages and asserting set-equality — is the highest
// enforcement reachable for a cross-layer literal mirror, the same permanent
// ceiling family as the holder-seal won't-do issues #851 / #893 / #1282.
package archtest

import (
	"reflect"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

func TestIdempotencyFrameworkStatusOracleAlign01(t *testing.T) {
	runtimeSet := append([]int(nil), idemhttp.FrameworkStatuses()...)
	sort.Ints(runtimeSet)

	// Anti-vacuity / golden anchor: a pure cross-equality check would still pass
	// if BOTH the runtime source and the kernel oracle drifted together to a
	// wrong-but-equal value (e.g. both → {409, 423}). Anchor the runtime source to
	// the known-good set so any drift on either side is caught. Update this golden
	// only alongside a deliberate framework-status change.
	wantGolden := []int{409, 422} // 409 ClaimBusy, 422 key-reused
	if !reflect.DeepEqual(runtimeSet, wantGolden) {
		t.Fatalf("idemhttp.FrameworkStatuses()=%v, want golden %v (update the golden only "+
			"alongside a deliberate framework-status change)", runtimeSet, wantGolden)
	}

	// The kernel single literal source (referenced by the oracle and CH-07) must
	// mirror the runtime set.
	canonical := append([]int(nil), metadata.FrameworkIdempotencyStatuses()...)
	sort.Ints(canonical)
	if !reflect.DeepEqual(canonical, runtimeSet) {
		t.Fatalf("metadata.FrameworkIdempotencyStatuses()=%v but idemhttp.FrameworkStatuses()=%v — "+
			"the kernel literal must mirror the runtime source", canonical, runtimeSet)
	}

	// A PrincipalUser-reachable mutating route (no auth flags, non-internal path)
	// computes the same set; every mutating method shares it, POST is representative.
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		oracle := append([]int(nil), (&metadata.HTTPTransportMeta{Method: method}).IdempotencyFrameworkStatuses()...)
		sort.Ints(oracle)
		if !reflect.DeepEqual(oracle, runtimeSet) {
			t.Fatalf("CH-07 oracle drift (%s): metadata.IdempotencyFrameworkStatuses()=%v but "+
				"idemhttp.FrameworkStatuses()=%v — the kernel literal must mirror the runtime source",
				method, oracle, runtimeSet)
		}
	}
}
