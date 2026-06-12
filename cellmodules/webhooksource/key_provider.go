package webhooksource

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

// Key-provider enum values for GOCELL_WEBHOOK_KEY_PROVIDER. Single source of truth
// for the switch dispatch and the slog provider label.
const (
	providerLocalAES     = "local-aes"
	providerVaultTransit = "vault-transit"
)

// buildKeyProviderFromName is the adapter-aware key-provider factory for the
// webhook source secret encryption key (#1540). It deliberately mirrors
// cellmodules/configcore's buildKeyProviderFromName rather than sharing it: there
// are only two consumers (configcore + webhook), and the go-standards "重复三次
// 才抽" guideline defers extraction until a third appears. Both consumers reuse
// the same underlying primitives (runtime/crypto local-aes, adapters/vault
// transit, cellsecrets demo-key rejection); only the env-var namespace differs.
//
// Unlike configcore's counterpart, this function takes no storageBackend
// argument: the loader only calls it in postgres-storage mode (memory/demo
// deployments use the in-memory kwh.SourceRegistry directly), so there is no
// NoopTransformer path and an empty provider is ALWAYS an error here — persisting
// an unencrypted webhook secret is never acceptable.
//
// The vault-transit branch reuses the SAME vault transit metric names as
// configcore; the kernel MetricsProvider returns the already-registered
// collectors on a duplicate name (prometheus AlreadyRegisteredError reuse), so a
// deployment running both configcore and webhook on vault-transit does not fail.
func buildKeyProviderFromName(
	adapterMode, providerName, masterKey, prevMasterKey string,
	clk clock.Clock,
	metricsProvider metrics.Provider,
) (kcrypto.KeyProvider, error) {
	switch providerName {
	case providerLocalAES:
		return buildLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey)
	case providerVaultTransit:
		return buildVaultTransitKeyProvider(adapterMode, clk, metricsProvider)
	case "":
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"webhooksource: GOCELL_WEBHOOK_KEY_PROVIDER must be set for the persistent "+
				"webhook source store (known values: \"local-aes\" for dev/CI, "+
				"\"vault-transit\" for production)")
	default:
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_WEBHOOK_KEY_PROVIDER; known values: \"local-aes\", \"vault-transit\"",
			errcode.WithDetails(errcode.PublicString("provider", providerName)))
	}
}

// buildLocalAESKeyProvider constructs the local-aes KeyProvider, rejecting
// well-known demo keys in real adapter mode.
func buildLocalAESKeyProvider(adapterMode, masterKey, prevMasterKey string) (kcrypto.KeyProvider, error) {
	lowerMK := []byte(strings.ToLower(masterKey))
	if err := cellsecrets.RejectDemoKey(adapterMode, "GOCELL_WEBHOOK_MASTER_KEY", lowerMK); err != nil {
		return nil, err
	}
	if prevMasterKey != "" {
		lowerPrev := []byte(strings.ToLower(prevMasterKey))
		if err := cellsecrets.RejectDemoKey(adapterMode, "GOCELL_WEBHOOK_MASTER_KEY_PREVIOUS", lowerPrev); err != nil {
			return nil, err
		}
	}
	kp, err := crypto.NewLocalAESKeyProviderFromKeys(masterKey, prevMasterKey)
	if err != nil {
		return nil, fmt.Errorf("webhooksource local-aes key provider: %w", err)
	}
	slog.Info("webhooksource: key provider initialized", slog.String("provider", providerLocalAES))
	return kp, nil
}

// buildVaultTransitKeyProvider constructs the vault-transit KeyProvider from env.
func buildVaultTransitKeyProvider(
	adapterMode string, clk clock.Clock, metricsProvider metrics.Provider,
) (kcrypto.KeyProvider, error) {
	m, err := adaptervault.NewTransitMetrics(metricsProvider)
	if err != nil {
		return nil, fmt.Errorf("webhooksource vault-transit metrics: %w", err)
	}
	kp, err := adaptervault.NewTransitKeyProviderFromEnv(cellsecrets.IsRealMode(adapterMode), clk, m)
	if err != nil {
		return nil, fmt.Errorf("webhooksource vault-transit key provider: %w", err)
	}
	slog.Info("webhooksource: key provider initialized", slog.String("provider", providerVaultTransit))
	return kp, nil
}
