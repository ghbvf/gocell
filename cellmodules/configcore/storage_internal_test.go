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

// TestBuildConfigCoreResult_ManagedKeyProvider_SingleSourceResource locks the
// single-source contract (PR #591 / #1420): when the configcore KeyProvider
// implements ManagedResource it is returned ONLY in the Resources channel (3rd
// return). buildConfigCoreResult no longer calls bootstrap.WithManagedResource
// (banned in cellmodules/ by WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01) — the
// Builder derives BOTH the steady-state WithManagedResource registration (its
// Probes() become /readyz checks + a LIFO teardown) AND the pre-Run rollback
// from this one Resources entry. Before #1420 the provider was written to two
// channels and forgetting either half silently regressed (leak / dropped probe);
// now both halves come from one source so they cannot diverge.
func TestBuildConfigCoreResult_ManagedKeyProvider_SingleSourceResource(t *testing.T) {
	kp := fakeManagedKeyProvider{}

	_, opts, resources := buildConfigCoreResult(nil, kp, configCoreModuleResult{})

	// Single source: the managed KeyProvider is the one Resources entry.
	require.Len(t, resources, 1, "managed KeyProvider must be returned as a Resources entry (single source)")
	assert.Equal(t, kernellifecycle.ManagedResource(kp), resources[0], "the resource must be the KeyProvider itself")

	// No WithManagedResource is added here — the Builder derives it from
	// Resources. With an empty configCoreModuleResult (no relay opt), opts is empty.
	assert.Empty(t, opts, "buildConfigCoreResult must not call WithManagedResource; Builder funnels Resources")
}

// TestBuildConfigCoreResult_PlainKeyProvider_NoResource verifies the inverse:
// a KeyProvider that is NOT a ManagedResource must not appear in the Resources
// channel (no spurious probe, no rollback entry) and adds no option.
func TestBuildConfigCoreResult_PlainKeyProvider_NoResource(t *testing.T) {
	kp := fakePlainKeyProvider{}

	_, opts, resources := buildConfigCoreResult(nil, kp, configCoreModuleResult{})

	assert.Empty(t, resources, "non-managed KeyProvider must not appear in the Resources channel")
	assert.Empty(t, opts, "non-managed KeyProvider must not add any option")
}
