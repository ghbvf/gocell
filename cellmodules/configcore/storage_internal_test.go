package configcore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
)

// errFakeKeyProvider is returned by the fake KeyProvider crypto methods; the
// test only exercises buildConfigCoreResult's type assertion, never the crypto
// path, so a sentinel keeps the fakes nilnil-clean without real key material.
var errFakeKeyProvider = errors.New("fake key provider: crypto path not exercised")

// fakePlainKeyProvider implements kcrypto.KeyProvider only.
type fakePlainKeyProvider struct{}

func (fakePlainKeyProvider) Current(context.Context) (kcrypto.KeyHandle, error) {
	return nil, errFakeKeyProvider
}

func (fakePlainKeyProvider) ByID(context.Context, string) (kcrypto.KeyHandle, error) {
	return nil, errFakeKeyProvider
}

func (fakePlainKeyProvider) Rotate(context.Context) (string, error) { return "", nil }

// fakeManagedKeyProvider implements kcrypto.KeyProvider AND
// kernellifecycle.ManagedResource — modeling the vault-transit KeyProvider,
// the only production provider that participates in the bootstrap lifecycle
// (local-aes does not implement ManagedResource).
type fakeManagedKeyProvider struct {
	fakePlainKeyProvider
}

func (fakeManagedKeyProvider) Probes() []healthz.Probe {
	return []healthz.Probe{
		healthz.NewProbe(healthz.MustProbeName("fake_keyprovider_ready"), func(context.Context) error { return nil }),
	}
}
func (fakeManagedKeyProvider) Worker() worker.Worker       { return nil }
func (fakeManagedKeyProvider) Close(context.Context) error { return nil }

var (
	_ kcrypto.KeyProvider             = fakePlainKeyProvider{}
	_ kcrypto.KeyProvider             = fakeManagedKeyProvider{}
	_ kernellifecycle.ManagedResource = fakeManagedKeyProvider{}
)

// TestBuildConfigCoreResult_ManagedKeyProvider_DualWrite is the F4 regression:
// when the configcore KeyProvider implements ManagedResource it MUST be written
// to BOTH channels — the provisional slice (pre-Run rollback) and a
// bootstrap.Option (WithManagedResource → bootstrap registers its Probes() as
// /readyz health probes and a LIFO teardown). Dropping either half silently
// regresses: missing provisional leaks the provider on sibling-module failure;
// missing the opt drops its readiness probe from /readyz and skips shutdown
// Close. The opt→probe expansion itself is covered by bootstrap's
// managed_resource_test.go; this test locks configcore's dual-write decision.
func TestBuildConfigCoreResult_ManagedKeyProvider_DualWrite(t *testing.T) {
	kp := fakeManagedKeyProvider{}

	_, opts, provisional := buildConfigCoreResult(nil, kp, configCoreModuleResult{})

	// Rollback channel: directly inspectable.
	require.Len(t, provisional, 1, "managed KeyProvider must be returned as a provisional resource for rollback")
	assert.Equal(t, kernellifecycle.ManagedResource(kp), provisional[0], "the provisional resource must be the KeyProvider itself")

	// Happy-path channel: WithManagedResource is opaque, so assert exactly one
	// bootstrap.Option was appended on top of the (empty) modResult opts.
	require.Len(t, opts, 1, "managed KeyProvider must add exactly one WithManagedResource bootstrap.Option")
}

// TestBuildConfigCoreResult_PlainKeyProvider_NoManagedWrite verifies the inverse:
// a KeyProvider that is NOT a ManagedResource must not be registered in either
// channel (no spurious probe, no rollback entry).
func TestBuildConfigCoreResult_PlainKeyProvider_NoManagedWrite(t *testing.T) {
	kp := fakePlainKeyProvider{}

	_, opts, provisional := buildConfigCoreResult(nil, kp, configCoreModuleResult{})

	assert.Empty(t, provisional, "non-managed KeyProvider must not appear in the rollback channel")
	assert.Empty(t, opts, "non-managed KeyProvider must not add a WithManagedResource option")
}
