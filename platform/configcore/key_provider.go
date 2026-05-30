package configcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/platform/platformshared"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// buildKeyProvider constructs the KeyProvider from the supplied providerName
// and key material.
//
// Supported providerName values: "local-aes", "vault-transit".
// In memory mode (empty providerName) a no-key sentinel is returned.
// In postgres mode (empty providerName) the function fails fast.
//
// vaultMetrics lazily provides the vault-transit metric set; only the
// vault-transit branch invokes it.
func buildKeyProvider(
	storageBackend, adapterMode, providerName, masterKey, prevMasterKey string, clk clock.Clock,
	vaultMetrics func() (*adaptervault.TransitMetrics, error),
) (kcrypto.KeyProvider, error) {
	if providerName == "" {
		if storageBackend == "postgres" {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing,
				"configcore: GOCELL_CONFIGCORE_KEY_PROVIDER must be set when "+
					"StorageBackend=postgres (known values: \"local-aes\" for "+
					"dev/CI, \"vault-transit\" for production). Silent "+
					"NoopTransformer fallback is disabled because it would "+
					"persist sensitive values unencrypted.")
		}
		return noKeyProvider{}, nil
	}
	switch providerName {
	case "local-aes":
		return buildLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey)
	case "vault-transit":
		return buildVaultTransitKeyProvider(adapterMode, clk, vaultMetrics)
	default:
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_CONFIGCORE_KEY_PROVIDER; known values: \"local-aes\", \"vault-transit\"",
			errcode.WithDetails(errcode.PublicString("provider", providerName)))
	}
}

// buildLocalAESKeyProvider constructs the local-aes KeyProvider.
func buildLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey string) (kcrypto.KeyProvider, error) {
	lowerMK := []byte(strings.ToLower(masterKey))
	if err := platformshared.RejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY", lowerMK); err != nil {
		return nil, err
	}
	if prevMasterKey != "" {
		lowerPrev := []byte(strings.ToLower(prevMasterKey))
		if err := platformshared.RejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", lowerPrev); err != nil {
			return nil, err
		}
	}
	kp, err := crypto.NewLocalAESKeyProviderFromKeys(masterKey, prevMasterKey)
	if err != nil {
		return nil, fmt.Errorf("local-aes key provider: %w", err)
	}
	slog.Info("configcore: key provider initialized", slog.String("provider", "local-aes"))
	return kp, nil
}

type vaultMetricsFactory func() (*adaptervault.TransitMetrics, error)

// buildVaultTransitKeyProvider constructs the vault-transit KeyProvider.
func buildVaultTransitKeyProvider(
	adapterMode string, clk clock.Clock, vaultMetrics vaultMetricsFactory,
) (kcrypto.KeyProvider, error) {
	metrics, err := vaultMetrics()
	if err != nil {
		return nil, err
	}
	kp, err := adaptervault.NewTransitKeyProviderFromEnv(platformshared.IsRealMode(adapterMode), clk, metrics)
	if err != nil {
		return nil, fmt.Errorf("vault-transit key provider: %w", err)
	}
	slog.Info("configcore: key provider initialized", slog.String("provider", "vault-transit"))
	return kp, nil
}

// keyProviderToTransformer wraps a KeyProvider in a ValueTransformer.
func keyProviderToTransformer(kp kcrypto.KeyProvider) kcrypto.ValueTransformer {
	if kp == nil || isNoKeyProvider(kp) {
		return crypto.NoopTransformer{}
	}
	return crypto.NewValueTransformer(kp)
}

type noKeyProvider struct{}

const noKeyProviderConfiguredMessage = "configcore: no key provider configured"

func (noKeyProvider) Current(context.Context) (kcrypto.KeyHandle, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing, noKeyProviderConfiguredMessage)
}

func (noKeyProvider) ByID(context.Context, string) (kcrypto.KeyHandle, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing, noKeyProviderConfiguredMessage)
}

func (noKeyProvider) Rotate(context.Context) (string, error) {
	return "", errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing, noKeyProviderConfiguredMessage)
}

func isNoKeyProvider(kp kcrypto.KeyProvider) bool {
	_, ok := kp.(noKeyProvider)
	return ok
}
