package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

func TestAllow_RoundTrip(t *testing.T) {
	t.Parallel()
	obs := Obligations{
		RowScope:  tenant.RowScopeTenant,
		FieldMask: FieldMask{Fields: []string{"ssn", "phone"}},
	}
	d, err := Allow(obs)
	require.NoError(t, err)

	assert.True(t, d.IsAllow(), "Allow() must return an allow decision")
	assert.Equal(t, EffectAllow, d.Effect())
	assert.Equal(t, obs.RowScope, d.Obligations().RowScope)
	assert.Equal(t, obs.FieldMask.Fields, d.Obligations().FieldMask.Fields)
	assert.Equal(t, "", d.Reason(), "Allow reason must be empty")
}

func TestAllow_EmptyObligations(t *testing.T) {
	t.Parallel()
	d, err := Allow(Obligations{})
	require.NoError(t, err)
	assert.True(t, d.IsAllow())
	assert.True(t, d.Obligations().FieldMask.IsZero())
	assert.Equal(t, tenant.RowScope(0), d.Obligations().RowScope)
}

// TestAllow_InvalidObligations verifies that Allow() with an invalid
// Obligations returns an error and a zero (deny) Decision (fail-closed).
func TestAllow_InvalidObligations(t *testing.T) {
	t.Parallel()
	// RowScope(99) is out of the valid range {self, device, tenant, all}.
	d, err := Allow(Obligations{RowScope: tenant.RowScope(99)})
	require.Error(t, err, "Allow with invalid RowScope must return error")
	assert.False(t, d.IsAllow(), "zero Decision on error must be non-Allow (fail-closed)")
}

func TestDeny_RoundTrip(t *testing.T) {
	t.Parallel()
	d := Deny("policy abc:123 forbids action read on resource device/42")

	assert.False(t, d.IsAllow(), "Deny() must return a deny decision")
	assert.Equal(t, EffectDeny, d.Effect())
	assert.Equal(t, "policy abc:123 forbids action read on resource device/42", d.Reason())
}

func TestDeny_EmptyReason(t *testing.T) {
	t.Parallel()
	d := Deny("")
	assert.False(t, d.IsAllow())
	assert.Equal(t, "", d.Reason())
}

func TestDeny_ObligationsAreEmpty(t *testing.T) {
	t.Parallel()
	d := Deny("some reason")
	// Obligations on a deny decision carry no semantic meaning; they should be
	// their zero values.
	assert.Equal(t, tenant.RowScope(0), d.Obligations().RowScope)
	assert.True(t, d.Obligations().FieldMask.IsZero())
}

// TestDecision_ZeroValueFailClosed is the critical safety test: a zero-value
// Decision (Decision{}) must behave as deny (fail-closed). Any consumer checking
// d.IsAllow() on an uninitialized Decision must NOT inadvertently grant access.
func TestDecision_ZeroValueFailClosed(t *testing.T) {
	t.Parallel()
	var d Decision
	assert.False(t, d.IsAllow(), "zero-value Decision must not allow (fail-closed)")
	assert.NotEqual(t, EffectAllow, d.Effect(), "zero-value Decision Effect must not be EffectAllow")
}

func TestDecision_EffectAccessor(t *testing.T) {
	t.Parallel()
	allowDec, err := Allow(Obligations{})
	require.NoError(t, err)
	tests := []struct {
		name       string
		d          Decision
		wantEffect Effect
		wantAllow  bool
	}{
		{"Allow", allowDec, EffectAllow, true},
		{"Deny with reason", Deny("denied"), EffectDeny, false},
		{"Deny empty reason", Deny(""), EffectDeny, false},
		{"zero value", Decision{}, Effect(0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantEffect, tc.d.Effect())
			assert.Equal(t, tc.wantAllow, tc.d.IsAllow())
		})
	}
}

// TestDecision_SealedConstruction verifies that the Decision fields cannot be
// accessed or set from outside the package via struct literals (compile-time
// guarantee, but we also verify runtime behavior via the constructors).
func TestDecision_SealedConstruction(t *testing.T) {
	t.Parallel()
	// The only construction paths are Allow() and Deny(). Any forged literal
	// would fail to compile outside this package. We verify Allow/Deny are the
	// only meaningful values.
	allowDecision, err := Allow(Obligations{RowScope: tenant.RowScopeSelf})
	require.NoError(t, err)
	denyDecision := Deny("test")

	assert.True(t, allowDecision.IsAllow())
	assert.False(t, denyDecision.IsAllow())

	// Verify that the Allow decision carries obligations through.
	assert.Equal(t, tenant.RowScopeSelf, allowDecision.Obligations().RowScope)
	// Verify that Deny does not carry obligations.
	assert.Equal(t, tenant.RowScope(0), denyDecision.Obligations().RowScope)
}

// TestDecision_ObligationsMutationIsolation verifies F2: mutating the original
// Obligations after Allow() must NOT affect the stored verdict, and mutating
// the slice returned by Obligations() must NOT affect a fresh call.
func TestDecision_ObligationsMutationIsolation(t *testing.T) {
	t.Parallel()

	t.Run("mutating original obligations after Allow does not affect Decision", func(t *testing.T) {
		t.Parallel()
		original := Obligations{
			RowScope:  tenant.RowScopeTenant,
			FieldMask: FieldMask{Fields: []string{"ssn", "phone"}},
		}
		dec, err := Allow(original)
		require.NoError(t, err)

		// Mutate the original slice element.
		original.FieldMask.Fields[0] = "TAMPERED"

		// The Decision must still see the original value.
		got := dec.Obligations().FieldMask.Fields[0]
		assert.Equal(t, "ssn", got,
			"mutating the original Obligations.FieldMask.Fields must not affect the stored Decision")
	})

	t.Run("mutating the returned Obligations does not affect a fresh Obligations() call", func(t *testing.T) {
		t.Parallel()
		dec, err := Allow(Obligations{
			RowScope:  tenant.RowScopeTenant,
			FieldMask: FieldMask{Fields: []string{"ssn", "phone"}},
		})
		require.NoError(t, err)

		// Mutate the first returned copy.
		first := dec.Obligations()
		first.FieldMask.Fields[0] = "TAMPERED"

		// A fresh call must return the unmodified value.
		second := dec.Obligations()
		assert.Equal(t, "ssn", second.FieldMask.Fields[0],
			"mutating a previously returned Obligations must not affect a fresh Obligations() call")
	})
}
