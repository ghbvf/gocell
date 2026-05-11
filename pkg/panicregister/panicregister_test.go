package panicregister_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

func TestApproved_TransparentPassThrough(t *testing.T) {
	errVal := errors.New("some error")
	errCodeVal := errcode.Assertion("test assertion")

	cases := []struct {
		name   string
		reason string
		value  any
		check  func(t *testing.T, got any)
	}{
		{
			name:   "string value",
			reason: "test-string-passthrough",
			value:  "hello",
			check: func(t *testing.T, got any) {
				assert.Equal(t, "hello", got)
			},
		},
		{
			name:   "error pointer",
			reason: "test-error-passthrough",
			value:  errVal,
			check: func(t *testing.T, got any) {
				assert.Same(t, errVal, got.(error))
			},
		},
		{
			name:   "errcode.Error non-nil pointer",
			reason: "test-errcode-passthrough",
			value:  errCodeVal,
			check: func(t *testing.T, got any) {
				assert.Same(t, errCodeVal, got.(*errcode.Error))
			},
		},
		{
			name:   "errcode.Error typed nil",
			reason: "test-errcode-typed-nil",
			value:  (*errcode.Error)(nil),
			check: func(t *testing.T, got any) {
				assert.Nil(t, got)
			},
		},
		{
			name:   "struct value",
			reason: "test-struct-passthrough",
			value:  struct{ X int }{X: 42},
			check: func(t *testing.T, got any) {
				assert.Equal(t, struct{ X int }{X: 42}, got)
			},
		},
		{
			name:   "nil value",
			reason: "test-nil-passthrough",
			value:  nil,
			check: func(t *testing.T, got any) {
				assert.Nil(t, got)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := panicregister.Approved(tc.reason, tc.value)
			tc.check(t, got)
		})
	}
}

func TestApproved_ReasonDoesNotAffectReturn(t *testing.T) {
	cases := []struct {
		name    string
		reasonA string
		reasonB string
		value   any
	}{
		{
			name:    "different reasons same string value",
			reasonA: "a",
			reasonB: "b",
			value:   "x",
		},
		{
			name:    "different reasons same int value",
			reasonA: "reason-one",
			reasonB: "reason-two",
			value:   99,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotA := panicregister.Approved(tc.reasonA, tc.value)
			gotB := panicregister.Approved(tc.reasonB, tc.value)
			assert.Equal(t, gotA, gotB)
		})
	}
}

// TestApproved_PanicRecoverPreservesType verifies that panic(Approved(...))
// preserves the identity of the wrapped value: recover() returns the exact
// pointer that Approved received. Documents the funnel's runtime transparency.
func TestApproved_PanicRecoverPreservesType(t *testing.T) {
	want := errcode.Assertion("test message")
	var got any
	func() {
		defer func() { got = recover() }()
		panic(panicregister.Approved("panicregister-recover-passthrough", want))
	}()
	require.NotNil(t, got, "expected panic")
	require.Same(t, want, got.(*errcode.Error))
}

// TestApproved_PanicRecoverSemantics_TypedNil documents the runtime behavior
// when Approved wraps a typed-nil pointer. Go's panic/recover model:
//   - panic((*T)(nil)) is *not* the same as panic(nil); the recovered
//     interface{} carries a non-nil itab (dynamic type=*T) with a nil value.
//   - panic(nil) since Go 1.21 produces *runtime.PanicNilError; Approved
//     transparently passes nil through (Go runtime handles the special case).
//
// Recovery middleware paths in kernel/wrapper and runtime/http/middleware
// rely on this behavior — recover() returning a non-nil interface{} for
// typed-nil panic values means panicAsError can attempt a type assertion
// without false negatives.
//
// Note: testify's require.NotNil / assert.Nil use reflection and consider a
// typed-nil pointer stored in an interface{} as "nil" (they inspect the
// underlying pointer value). We use reflect.ValueOf directly to assert that
// the interface itself is non-nil at the Go language level.
func TestApproved_PanicRecoverSemantics_TypedNil(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		panic(panicregister.Approved("panicregister-typed-nil-recover-test", (*errcode.Error)(nil)))
	}()
	// At the Go language level the interface is non-nil: it has a dynamic type
	// (*errcode.Error) even though the pointer value is nil. Use reflect to
	// assert this — testify's NotNil considers typed-nil interfaces as nil.
	rv := reflect.ValueOf(recovered)
	require.True(t, rv.IsValid(), "typed-nil panic should yield non-nil interface after recover")
	asErr, ok := recovered.(*errcode.Error)
	require.True(t, ok, "dynamic type must be *errcode.Error")
	require.Nil(t, asErr, "wrapped pointer value must remain nil")
}
