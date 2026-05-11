package panicregister_test

import (
	"errors"
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
