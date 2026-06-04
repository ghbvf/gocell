package auth

import (
	"sync/atomic"
	"testing"
)

// TestAuthPlanKind_DiscriminantParity exercises every implementation's
// unexported authPlanKind() marker from inside the package. Beyond locking
// the type↔discriminant mapping (AuthNone↔AuthKindNone, etc.), the call also
// participates in the package coverage profile: authPlanKind is an unexported
// sealing marker that production code never calls at runtime, so the only
// way to surface it in `go test -cover` is from a same-package test.
//
// If a new AuthPlan implementation is added without a matching AuthKind*
// constant — or vice versa — the table below stays out of sync and this
// test starts failing.
func TestAuthPlanKind_DiscriminantParity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		plan AuthPlan
		want AuthKind
	}{
		{"AuthNone", AuthNone{}, AuthKindNone},
		{"AuthJWT", AuthJWT{}, AuthKindJWT},
		{"AuthJWTFromAssembly", AuthJWTFromAssembly{}, AuthKindJWTFromAssembly},
		{"AuthMTLS", AuthMTLS{}, AuthKindMTLS},
		{"AuthServiceToken", AuthServiceToken{}, AuthKindServiceToken},
		{"AuthOperator", AuthOperator{}, AuthKindOperator},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.plan.authPlanKind(); got != tc.want {
				t.Errorf("%T.authPlanKind() = %d, want %d", tc.plan, got, tc.want)
			}
		})
	}
}

// TestListenerAuthSeal_ExercisesMarker calls the unexported listenerAuthOK()
// marker on every ListenerAuth implementation. The marker is a no-op (its
// existence at compile time is what closes the sealed interface) but it must
// participate in the coverage profile — otherwise the package coverage dips
// below the kernel/ 90% floor whenever this file is the dominant code path.
func TestListenerAuthSeal_ExercisesMarker(t *testing.T) {
	t.Parallel()
	plans := []ListenerAuth{
		AuthNone{},
		AuthJWT{},
		AuthJWTFromAssembly{},
		AuthMTLS{},
		AuthServiceToken{},
		AuthOperator{},
	}
	for _, p := range plans {
		p.listenerAuthOK() // no-op marker; the call is the coverage signal
	}
}

// TestAuthJWTFromAssembly_ResolvedVerifier_ZeroValue covers the defensive
// `p.resolved == nil` branch of ResolvedVerifier — taken when callers use
// the struct as a zero value rather than going through NewAuthJWTFromAssembly.
// The error-first constructor sets resolved to a non-nil atomic.Pointer, so
// the bootstrap.WithAssembly happy path never exercises this branch.
//
// The other two branches (constructed + unresolved, constructed + resolved)
// are covered by TestAuthJWTFromAssembly_ResolvedVerifier in auth_plan_test.go.
func TestAuthJWTFromAssembly_ResolvedVerifier_ZeroValue(t *testing.T) {
	t.Parallel()
	if v := (AuthJWTFromAssembly{}).ResolvedVerifier(); v != nil {
		t.Errorf("zero-value AuthJWTFromAssembly.ResolvedVerifier() = %v, want nil", v)
	}
	// Sanity probe: a struct-literal AuthJWTFromAssembly with a non-nil
	// resolved pointer but no Store call must also return nil so callers can
	// fail-fast at bootstrap phase4 — exercises the `vp == nil` branch
	// explicitly (the constructor + un-set-resolved path already covers it,
	// but pinning here makes the invariant local to this file).
	p := AuthJWTFromAssembly{resolved: &atomic.Pointer[IntentTokenVerifier]{}}
	if v := p.ResolvedVerifier(); v != nil {
		t.Errorf("unresolved AuthJWTFromAssembly.ResolvedVerifier() = %v, want nil", v)
	}
}
