package auth

import (
	"bytes"
	"context"
	"testing"
)

// fakeOperatorLimiter is a minimal OperatorRateLimiter for constructor tests.
type fakeOperatorLimiter struct{ allow bool }

func (f fakeOperatorLimiter) Allow(string) bool { return f.allow }

type newAuthOperatorCase struct {
	name     string
	username []byte
	password []byte
	limiter  OperatorRateLimiter
	onFail   func(context.Context, string)
	wantErr  bool
}

func TestNewAuthOperator(t *testing.T) {
	t.Parallel()
	lim := fakeOperatorLimiter{allow: true}
	obs := func(context.Context, string) {}

	cases := []newAuthOperatorCase{
		{"valid with observer", []byte("ops"), []byte("s3cretpwd"), lim, obs, false},
		{"valid nil observer ok", []byte("ops"), []byte("s3cretpwd"), lim, nil, false},
		{"min-length password (8) accepted", []byte("ops"), []byte("8bytespw"), lim, obs, false},
		{"empty username", nil, []byte("s3cretpwd"), lim, obs, true},
		{"empty password", []byte("ops"), []byte(""), lim, obs, true},
		{"one-byte password rejected", []byte("ops"), []byte("x"), lim, obs, true},
		{"short password (7) rejected", []byte("ops"), []byte("7bytesp"), lim, obs, true},
		{"control-char username rejected", []byte("ops\n"), []byte("s3cretpwd"), lim, obs, true},
		{"nil limiter (bare)", []byte("ops"), []byte("s3cretpwd"), nil, obs, true},
		{"nil limiter (typed)", []byte("ops"), []byte("s3cretpwd"), OperatorRateLimiter(nil), obs, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertNewAuthOperator(t, tc)
		})
	}
}

func assertNewAuthOperator(t *testing.T, tc newAuthOperatorCase) {
	t.Helper()

	got, err := NewAuthOperator(tc.username, tc.password, tc.limiter, tc.onFail)
	if tc.wantErr {
		if err == nil {
			t.Fatalf("NewAuthOperator(%q) = nil error, want error", tc.name)
		}
		return
	}
	if err != nil {
		t.Fatalf("NewAuthOperator(%q) unexpected error: %v", tc.name, err)
	}
	if !bytes.Equal(got.Username, tc.username) || !bytes.Equal(got.Password, tc.password) {
		t.Errorf("NewAuthOperator credentials not preserved: got %q/%q", got.Username, got.Password)
	}
	if got.Limiter == nil {
		t.Error("NewAuthOperator limiter not preserved")
	}
}

func TestAuthOperator_Describe(t *testing.T) {
	t.Parallel()
	if got := (AuthOperator{}).Describe(); got != "operator" {
		t.Errorf("AuthOperator.Describe() = %q, want %q", got, "operator")
	}
}

// TestAuthOperator_SatisfiesListenerAuth pins the sealed-interface membership at
// compile time (the var below) and exercises the plan via the interface.
func TestAuthOperator_SatisfiesListenerAuth(t *testing.T) {
	t.Parallel()
	var la ListenerAuth = AuthOperator{}
	if la.authPlanKind() != AuthKindOperator {
		t.Errorf("AuthOperator authPlanKind = %d, want %d", la.authPlanKind(), AuthKindOperator)
	}
}
