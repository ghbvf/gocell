package configcore

import (
	"fmt"
	"log/slog"
	"strings"

	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// buildKeyProviderFromName is the adapter-aware key-provider factory. It is
// self-contained within cellmodules/configcore (the Composition Root layer) so
// cmd/corebundle no longer needs to import adapters/vault.
//
// The vault-transit branch builds adapters/vault.TransitMetrics from the kernel
// MetricsProvider (post-#885 vault is client_golang-free in non-test code), so
// cellmodules/configcore can own this logic without importing
// github.com/prometheus/client_golang.
//
// Returns nil (no provider) when providerName is empty and storageBackend is
// not postgres. Callers must treat nil as "use NoopTransformer".
func buildKeyProviderFromName(
	storageBackend, adapterMode, providerName, masterKey, prevMasterKey string,
	clk clock.Clock,
	metricsProvider metrics.Provider,
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
		// Return nil → cellmodules/configcore will use NoopTransformer (memory mode).
		return nil, nil //nolint:nilnil // memory mode: nil KeyProvider is the documented no-key sentinel
	}
	switch providerName {
	case "local-aes":
		return buildLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey)
	case "vault-transit":
		return buildVaultTransitKeyProvider(adapterMode, clk, metricsProvider)
	default:
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_CONFIGCORE_KEY_PROVIDER; known values: \"local-aes\", \"vault-transit\"",
			errcode.WithDetails(errcode.PublicString("provider", providerName)))
	}
}

// buildLocalAESKeyProvider constructs the local-aes KeyProvider.
func buildLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey string) (kcrypto.KeyProvider, error) {
	lowerMK := []byte(strings.ToLower(masterKey))
	if err := cellsecrets.RejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY", lowerMK); err != nil {
		return nil, err
	}
	if prevMasterKey != "" {
		lowerPrev := []byte(strings.ToLower(prevMasterKey))
		if err := cellsecrets.RejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", lowerPrev); err != nil {
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

// buildVaultTransitKeyProvider constructs the vault-transit KeyProvider.
// Building vault metrics ONLY in this branch preserves the
// "memory/local-aes never register gocell_vault_* series" property.
func buildVaultTransitKeyProvider(
	adapterMode string, clk clock.Clock,
	metricsProvider metrics.Provider,
) (kcrypto.KeyProvider, error) {
	m, err := adaptervault.NewTransitMetrics(metricsProvider)
	if err != nil {
		return nil, fmt.Errorf("vault-transit metrics: %w", err)
	}
	kp, err := adaptervault.NewTransitKeyProviderFromEnv(cellsecrets.IsRealMode(adapterMode), clk, m)
	if err != nil {
		return nil, fmt.Errorf("vault-transit key provider: %w", err)
	}
	slog.Info("configcore: key provider initialized", slog.String("provider", "vault-transit"))
	return kp, nil
}
