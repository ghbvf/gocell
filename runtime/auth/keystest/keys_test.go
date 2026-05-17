package keystest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
)

func TestMustGenerateKeyPair_Roundtrip(t *testing.T) {
	t.Parallel()
	priv, pub := keystest.MustGenerateKeyPair()
	require.NotNil(t, priv)
	require.NotNil(t, pub)
	assert.GreaterOrEqual(t, pub.N.BitLen(), 2048,
		"generated key must satisfy MinRSAKeyBits")
}

func TestMustNewKeySet_HasSigningKeyID(t *testing.T) {
	t.Parallel()
	ks, priv, pub := keystest.MustNewKeySet(clock.Real())
	require.NotNil(t, ks)
	require.NotNil(t, priv)
	require.NotNil(t, pub)
	assert.NotEmpty(t, ks.SigningKeyID(),
		"NewKeySet must derive a non-empty signing key id")
}

func TestMustNewKeyProvider_ExposesBothDomains(t *testing.T) {
	t.Parallel()
	p := keystest.MustNewKeyProvider(clock.Real())
	require.NotNil(t, p)

	ks, err := p.RSAKeySet()
	require.NoError(t, err)
	assert.NotNil(t, ks)
	assert.NotEmpty(t, ks.SigningKeyID())

	ring, err := p.HMACKeyRing()
	require.NoError(t, err)
	assert.NotNil(t, ring)
	assert.GreaterOrEqual(t, len(ring.Current()), auth.MinHMACKeyBytes,
		"HMAC ring must satisfy minimum byte length")
}

// TestMustNewKeySet_PanicsOnNilClock verifies the panic plumbing of
// MustNewKeySet by passing a nil clock — auth.NewKeySet's MustHaveClock
// guard fires before any other validation, so this is the only reliable
// way to exercise the panic-recover wrapper from a test caller.
//
// The unreachable `if err != nil` branch inside MustNewKeySet (after
// auth.NewKeySet succeeds with non-nil priv/pub from MustGenerateKeyPair)
// has no testable trigger; documented as a defensive branch in keys.go
// godoc.
func TestMustNewKeySet_PanicsOnNilClock(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		keystest.MustNewKeySet(nil)
	}, "MustNewKeySet must panic when clock is nil")
}

// TestMustNewKeyProvider_PanicsOnNilClock asserts the panic plumbing
// surfaces through MustNewKeyProvider → MustNewKeySet → auth.NewKeySet
// (MustHaveClock guard).
func TestMustNewKeyProvider_PanicsOnNilClock(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		keystest.MustNewKeyProvider(nil)
	}, "MustNewKeyProvider must panic when clock is nil")
}
