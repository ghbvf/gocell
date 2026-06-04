// INVARIANT: IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01
//
// The kernel-side oracle metadata.HTTPTransportMeta.IdempotencyFrameworkStatuses()
// (a hand-written []int literal, because kernel/ must not import runtime/) MUST
// equal the single runtime source runtime/http/idempotency.FrameworkStatuses(),
// which derives its set from the actual sentinels the idempotency middleware
// emits (errInProgress → 409 ClaimBusy; ErrFingerprintMismatch → 422 key-reused).
//
// Why this exists: CH-07 (kernel/governance) forces every non-exempt mutating
// HTTP contract to declare the idempotency framework statuses returned by the
// oracle. The oracle is a literal that the middleware's runtime behavior can
// silently outgrow (e.g. a new injected status, or the 422 upgrade reverted).
// Binding the literal to the runtime source closes that drift: change the
// middleware-emitted set without updating the kernel literal (or vice versa) and
// this test fails.
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

	// Every mutating method shares the same framework-injected set; POST is
	// representative. The oracle is the kernel literal CH-07 enforces against.
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
