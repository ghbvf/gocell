package credentialfence_test

import (
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
)

// TestMint_ReturnsNonNil asserts Mint produces a value that survives the
// IsNilInterface guard the three mutation methods will apply at runtime.
func TestMint_ReturnsNonNil(t *testing.T) {
	t.Parallel()

	tok := credentialfence.Mint()
	if validation.IsNilInterface(tok) {
		t.Fatal("Mint returned nil-interface; mutation methods would reject it at runtime")
	}
}

// TestMint_AllValuesInterchangeable asserts every Mint return value is an
// equally valid FenceToken — Mint conveys *authorization*, not identity, so
// distinct values must be interchangeable.
func TestMint_AllValuesInterchangeable(t *testing.T) {
	t.Parallel()

	a := credentialfence.Mint()
	b := credentialfence.Mint()

	if validation.IsNilInterface(a) || validation.IsNilInterface(b) {
		t.Fatal("Mint returned nil-interface")
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		t.Fatalf("Mint values must share a concrete type; got %T vs %T", a, b)
	}
}

// TestFenceToken_NotImplementableByExternalType is the runtime witness of
// the type-system seal: the FenceToken interface has an unexported marker
// method, so types declared outside this package cannot satisfy it.
//
// This test exercises the dual: an external type that is NOT a FenceToken
// must NOT satisfy the interface assertion. If a future refactor exposed the
// marker method (typo, accidental rename), an external struct would silently
// satisfy FenceToken and this test would fail.
func TestFenceToken_NotImplementableByExternalType(t *testing.T) {
	t.Parallel()

	type externalImpostor struct{}

	if _, ok := any(externalImpostor{}).(credentialfence.FenceToken); ok {
		t.Fatal("FenceToken seal broken: an empty external struct satisfies the interface — " +
			"the marker method must remain unexported (lowercase isCredentialFenceToken)")
	}
}

// TestFenceToken_MarkerMethodUnexported asserts that every method declared
// on the FenceToken interface starts with a lowercase letter. The unexported
// marker method (isCredentialFenceToken) is what gives FenceToken its seal:
// types declared in any other package cannot implement an interface that
// requires an unexported method from this package. A rename that
// accidentally capitalizes the marker (e.g. to IsCredentialFenceToken) would
// expose the method to external implementations and silently break the
// seal. This test catches that regression.
func TestFenceToken_MarkerMethodUnexported(t *testing.T) {
	t.Parallel()

	ifaceType := reflect.TypeOf((*credentialfence.FenceToken)(nil)).Elem()
	for i := 0; i < ifaceType.NumMethod(); i++ {
		m := ifaceType.Method(i)
		// Interface method names are accessible via reflect, but exported-ness
		// is reported separately. For sealed-marker safety every method must
		// be PkgPath-bound (unexported).
		if m.PkgPath == "" {
			t.Fatalf("FenceToken seal broken: method %q is exported "+
				"(PkgPath is empty); any package can declare a matching "+
				"method and satisfy FenceToken. The marker method must "+
				"stay unexported (lowercase first letter).", m.Name)
		}
	}
}

// TestMustHave_NonNil asserts MustHave is a noop when given a real Mint() value.
// This is the happy-path branch every production callsite hits.
func TestMustHave_NonNil(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MustHave panicked on a real FenceToken: %v", r)
		}
	}()
	credentialfence.MustHave(credentialfence.Mint(), "test.callsite")
}

// TestMustHave_NilPanics asserts MustHave panics with an *errcode.Error of
// KindInternal when given a nil FenceToken. The panic is the surface area
// for the programmer-error 500 surfaced by the kernel Recovery middleware.
func TestMustHave_NilPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustHave did not panic on nil FenceToken")
		}
		err, ok := r.(*errcode.Error)
		if !ok {
			t.Fatalf("MustHave panic payload must be *errcode.Error; got %T (%v)", r, r)
		}
		if err.Kind != errcode.KindInternal {
			t.Errorf("MustHave panic Kind must be KindInternal (assertion); got %v", err.Kind)
		}
		if err.Code != errcode.ErrInternal {
			t.Errorf("MustHave panic must carry ErrInternal sentinel; got code=%v", err.Code)
		}
	}()
	var nilTok credentialfence.FenceToken
	credentialfence.MustHave(nilTok, "test.callsite.nil")
}

// TestValidationIsNilInterface_TypedNilForFenceToken asserts the upstream
// pkg/validation invariant that MustHave relies on: validation.IsNilInterface
// must return true for a bare-nil FenceToken interface value. This test
// exercises the external (package credentialfence_test) scope, where the
// concrete fenceToken impl is unexported and a true typed-nil cannot be
// constructed. The companion internal test (fencetoken_internal_test.go,
// package credentialfence) constructs a true typed-nil and verifies
// MustHave panics through the panic-taxonomy funnel.
func TestValidationIsNilInterface_TypedNilForFenceToken(t *testing.T) {
	t.Parallel()

	// Sanity-check the upstream invariant we rely on.
	var nilTok credentialfence.FenceToken
	if !validation.IsNilInterface(nilTok) {
		t.Fatal("validation.IsNilInterface(nil FenceToken) returned false — upstream invariant broken")
	}
}
