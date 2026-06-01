package main

import (
	"fmt"
	"log/slog"
	"strings"

	prom "github.com/prometheus/client_golang/prometheus"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// buildConfigCoreKeyProvider constructs the configcore KeyProvider and
// the stale-cipher counter callback from the supplied providerName, key
// material, prometheus registry, and vault-transit metrics factory.
//
// Supported providerName values: "local-aes", "vault-transit".
// When providerName is empty:
//   - memory mode → returns noKeyProvider sentinel (NoopTransformer path)
//   - postgres mode → fails fast (unencrypted persistence is rejected)
//
// The stale-cipher callback is always non-nil: it increments a prometheus
// counter when m.registry is non-nil, otherwise it is a silent no-op.
func buildConfigCoreKeyProvider(
	storageBackend, adapterMode, providerName, masterKey, prevMasterKey string,
	clk clock.Clock,
	registry *prom.Registry,
	vaultMetrics func() (*adaptervault.TransitMetrics, error),
) (kcrypto.KeyProvider, func(), error) {
	kp, err := buildKeyProviderFromName(
		storageBackend, adapterMode, providerName, masterKey, prevMasterKey, clk,
		vaultMetrics,
	)
	if err != nil {
		return nil, nil, err
	}

	incFn, err := buildStaleCipherInc(registry)
	if err != nil {
		return nil, nil, fmt.Errorf("configcore: register stale_cipher counter: %w", err)
	}
	return kp, incFn, nil
}

// buildKeyProviderFromName is the adapter-aware key-provider factory that lives
// in cmd/ rather than cellmodules/configcore because it imports adapters/vault
// (vault-transit path).
//
// Returns nil (no provider) when providerName is empty and storageBackend is
// not postgres. Callers must treat nil as "use NoopTransformer".
func buildKeyProviderFromName(
	storageBackend, adapterMode, providerName, masterKey, prevMasterKey string,
	clk clock.Clock,
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
		// Return nil → cellmodules/configcore will use NoopTransformer (memory mode).
		return nil, nil //nolint:nilnil // memory mode: nil KeyProvider is the documented no-key sentinel
	}
	switch providerName {
	case "local-aes":
		return buildCmdLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey)
	case "vault-transit":
		return buildCmdVaultTransitKeyProvider(adapterMode, clk, vaultMetrics)
	default:
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_CONFIGCORE_KEY_PROVIDER; known values: \"local-aes\", \"vault-transit\"",
			errcode.WithDetails(errcode.PublicString("provider", providerName)))
	}
}

// buildCmdLocalAESKeyProvider constructs the local-aes KeyProvider.
func buildCmdLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey string) (kcrypto.KeyProvider, error) {
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

// buildCmdVaultTransitKeyProvider constructs the vault-transit KeyProvider.
func buildCmdVaultTransitKeyProvider(
	adapterMode string, clk clock.Clock,
	vaultMetrics func() (*adaptervault.TransitMetrics, error),
) (kcrypto.KeyProvider, error) {
	metrics, err := vaultMetrics()
	if err != nil {
		return nil, err
	}
	kp, err := adaptervault.NewTransitKeyProviderFromEnv(cellsecrets.IsRealMode(adapterMode), clk, metrics)
	if err != nil {
		return nil, fmt.Errorf("vault-transit key provider: %w", err)
	}
	slog.Info("configcore: key provider initialized", slog.String("provider", "vault-transit"))
	return kp, nil
}

// configStaleCipherOpts is the Prometheus counter descriptor for M3 stale-key
// observability. Declared at package scope (not inline in buildStaleCipherInc)
// so the metricschema reachable-typed-metrics tool can statically resolve the
// CounterOpts literal — a function-local var is not a resolvable metric helper
// argument (TestBuild_CorebundleCapturesReachableTypedMetrics).
var configStaleCipherOpts = prom.CounterOpts{
	Namespace: "gocell",
	Subsystem: "config",
	Name:      "stale_cipher_total",
	Help:      "Number of config values read that are encrypted with a non-current key version.",
}

// buildStaleCipherInc registers (or reuses) the stale-cipher prometheus counter
// against registry and returns an Inc callback. When registry is nil, returns a
// silent no-op (acceptable in test environments).
func buildStaleCipherInc(registry *prom.Registry) (func(), error) {
	if registry == nil {
		return func() {}, nil
	}
	counter, err := promadapter.RegisterOrReuseCounter(registry, configStaleCipherOpts)
	if err != nil {
		return nil, err
	}
	return counter.Inc, nil
}
