package reconcile

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// resultSampleRequeue is a representative RequeueAfter magnitude for the
// normalization table (TEST-TIME-LITERAL-01: extracted to a package-level const).
const resultSampleRequeue = 5 * time.Second

// TestPermanentError_IsClassifiedNonRetry asserts the FR-003 happy path: an
// error wrapped by PermanentError is classified permanent and still unwraps to
// the original cause (so callers keep errors.Is reachability).
func TestPermanentError_IsClassifiedNonRetry(t *testing.T) {
	t.Parallel()
	base := errors.New("bad payload")
	pe := PermanentError(base)
	if !IsPermanent(pe) {
		t.Fatal("IsPermanent(PermanentError(...)) = false, want true")
	}
	if !errors.Is(pe, base) {
		t.Fatal("PermanentError must wrap the cause (errors.Is base) so the original error is recoverable")
	}
}

// TestPermanentError_NilPassThrough asserts PermanentError(nil) is a nil error
// (no spurious permanent wrapper) and IsPermanent(nil) is false.
func TestPermanentError_NilPassThrough(t *testing.T) {
	t.Parallel()
	if PermanentError(nil) != nil {
		t.Fatal("PermanentError(nil) must return nil")
	}
	if IsPermanent(nil) {
		t.Fatal("IsPermanent(nil) = true, want false")
	}
}

// TestIsPermanent_TransientIsFalse asserts a plain (transient) error is NOT
// classified permanent — the Loop must back off and retry it.
func TestIsPermanent_TransientIsFalse(t *testing.T) {
	t.Parallel()
	if IsPermanent(errors.New("transient")) {
		t.Fatal("plain error classified permanent, want transient (false)")
	}
}

// TestIsPermanent_SeesThroughWrapping asserts classification survives
// fmt.Errorf %w wrapping (a reconciler may add context around a permanent
// error before returning it).
func TestIsPermanent_SeesThroughWrapping(t *testing.T) {
	t.Parallel()
	pe := PermanentError(errors.New("root"))
	wrapped := fmt.Errorf("reconcile cert-%s: %w", "abc", pe)
	if !IsPermanent(wrapped) {
		t.Fatal("IsPermanent must see through fmt.Errorf %w wrapping")
	}
}

// foreignAsSpoof is an error that never passed through PermanentError but
// implements the errors.As As(any) bool hook to lie. It exists to prove the
// sealed-marker funnel holds: IsPermanent must classify it as NOT permanent.
// (A naive errors.As-based IsPermanent would be spoofed into reporting true —
// this test is also the regression guard against reverting to errors.As.)
type foreignAsSpoof struct{}

func (foreignAsSpoof) Error() string { return "foreign spoof" }
func (foreignAsSpoof) As(any) bool   { return true }

// TestIsPermanent_RejectsForeignAsSpoof asserts a foreign error cannot spoof the
// permanent classification via the errors.As As(any) bool hook — "permanent"
// stays unrepresentable outside this package.
func TestIsPermanent_RejectsForeignAsSpoof(t *testing.T) {
	t.Parallel()
	if IsPermanent(foreignAsSpoof{}) {
		t.Fatal("IsPermanent must NOT be spoofable by a foreign error's As(any) bool hook; " +
			"only errors that passed through PermanentError may classify permanent")
	}
	if IsPermanent(fmt.Errorf("ctx: %w", foreignAsSpoof{})) {
		t.Fatal("IsPermanent must not be spoofed through a %w chain either")
	}
}

// TestIsPermanent_SeesThroughJoinMultiTree asserts classification survives an
// errors.Join multi-error tree (Unwrap() []error): if any joined cause is
// permanent the aggregate is permanent (retrying cannot resolve the permanent
// branch), and a join of only transient causes stays transient.
func TestIsPermanent_SeesThroughJoinMultiTree(t *testing.T) {
	t.Parallel()
	pe := PermanentError(errors.New("revoked"))
	if !IsPermanent(errors.Join(errors.New("transient"), pe)) {
		t.Fatal("IsPermanent must see through errors.Join multi-error trees")
	}
	if IsPermanent(errors.Join(errors.New("a"), errors.New("b"))) {
		t.Fatal("errors.Join of only transient errors must classify transient")
	}
}

// TestResult_NormalizedRequeueAfter_NegativeClampedToZero asserts FR-002 /
// SC test T03: a negative RequeueAfter (programmer error) is treated as 0
// (== default tick) rather than panicking or scheduling in the past.
func TestResult_NormalizedRequeueAfter_NegativeClampedToZero(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"negative clamps to zero", -resultSampleRequeue, 0},
		{"zero stays zero", 0, 0},
		{"positive passes through", resultSampleRequeue, resultSampleRequeue},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Result{RequeueAfter: tc.in}.normalizedRequeueAfter()
			if got != tc.want {
				t.Fatalf("Result{RequeueAfter:%v}.normalizedRequeueAfter() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
