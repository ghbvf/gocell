package auth_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/framework/kernel/auth"
)

// TestPrincipalKindClaim_IsValid pins the closed enumeration of wire
// principal_kind values. The verifier calls IsValid fail-closed so an unknown
// value can never silently fall through to a user mint. Table mirrors the
// pattern in TestTokenIntent_IsValid (auth_plan_test.go).
func TestPrincipalKindClaim_IsValid(t *testing.T) {
	tests := []struct {
		kind  auth.PrincipalKindClaim
		valid bool
	}{
		{auth.PrincipalKindClaimUser, true},
		{auth.PrincipalKindClaimDevice, true},
		{auth.PrincipalKindClaim("bogus"), false},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind)+"_valid="+fmt.Sprintf("%v", tc.valid), func(t *testing.T) {
			assert.Equal(t, tc.valid, tc.kind.IsValid())
		})
	}
}

// TestNonceStoreKind_ReplaySafe pins the single source of truth for "which
// NonceStoreKind is replay-safe for a topology" (#1410 review F1/F2). Both
// composition.SharedDeps.validateProductionNonceStore (config-time, on the
// declared store) and runtime/bootstrap's phase0 auth-plan check (usage-time, on
// the store that actually guards the listener) gate on this predicate, so its
// truth table is the contract that keeps the two enforcement points from drifting.
//
// Fail-closed invariants this asserts:
//   - distributed is the only kind safe for multi-pod (requireDistributed=true);
//   - in-memory is safe ONLY single-pod;
//   - noop is never safe;
//   - an unrecognized kind is never safe (no permissive default).
func TestNonceStoreKind_ReplaySafe(t *testing.T) {
	tests := []struct {
		name               string
		kind               auth.NonceStoreKind
		requireDistributed bool
		want               bool
	}{
		{"distributed multi-pod", auth.NonceStoreKindDistributed, true, true},
		{"distributed single-pod", auth.NonceStoreKindDistributed, false, true},
		{"in-memory multi-pod rejected", auth.NonceStoreKindInMemory, true, false},
		{"in-memory single-pod accepted", auth.NonceStoreKindInMemory, false, true},
		{"noop multi-pod rejected", auth.NonceStoreKindNoop, true, false},
		{"noop single-pod rejected", auth.NonceStoreKindNoop, false, false},
		{"unknown multi-pod rejected fail-closed", auth.NonceStoreKind("totally-bogus"), true, false},
		{"unknown single-pod rejected fail-closed", auth.NonceStoreKind("totally-bogus"), false, false},
		{"empty kind rejected fail-closed", auth.NonceStoreKind(""), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.kind.ReplaySafe(tc.requireDistributed))
		})
	}
}
