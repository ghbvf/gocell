package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffect_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		e    Effect
		want bool
	}{
		{"zero value", 0, false},
		{"EffectAllow", EffectAllow, true},
		{"EffectDeny", EffectDeny, true},
		{"out-of-range high", Effect(100), false},
		{"out-of-range 3", Effect(3), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.e.Valid())
		})
	}
}

func TestEffect_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		e    Effect
		want string
	}{
		{"zero value", 0, "invalid"},
		{"EffectAllow", EffectAllow, "allow"},
		{"EffectDeny", EffectDeny, "deny"},
		{"out-of-range", Effect(99), "invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.e.String())
		})
	}
}

func TestEffect_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		e       Effect
		wantErr bool
	}{
		{"zero value is invalid", 0, true},
		{"EffectAllow is valid", EffectAllow, false},
		{"EffectDeny is valid", EffectDeny, false},
		{"out-of-range is invalid", Effect(100), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.e.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEffect_ZeroValueInotAllow(t *testing.T) {
	t.Parallel()
	var e Effect
	assert.NotEqual(t, EffectAllow, e, "zero Effect must not equal EffectAllow (fail-closed)")
}
