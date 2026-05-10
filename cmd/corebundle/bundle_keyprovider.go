package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// buildKeyProvider constructs the KeyProvider from the supplied providerName,
// masterKey, and prevMasterKey (all pre-read from per-cell env by the caller).
//
// Supported providerName values: "local-aes", "vault-transit".
// In memory mode (empty providerName) a no-key sentinel is returned (no
// encryption; NoopTransformer via keyProviderToTransformer).
// In postgres mode (empty providerName) the function fails-fast: sensitive-value
// encryption is a production security invariant; silently wiring NoopTransformer
// would defeat the stated threat model (see docs/architecture/202604191800-adr-config-value-encryption.md).
// Operators must explicitly opt in via GOCELL_CONFIGCORE_KEY_PROVIDER=local-aes
// (dev/CI only) or vault-transit (production).
//
// Note: buildKeyProvider intentionally keeps a positional-argument signature
// (unlike buildCursorCodec / buildHMACKey which use config structs) because
// its input set is small, fixed, and semantically distinct per argument
// (storageBackend determines whether encryption is required at all, the
// other four only matter when providerName == "local-aes"). Wrapping in a
// struct would obscure this branching logic. Revisit if a sixth argument is
// ever needed.
//
// ref: kubernetes/kubernetes pkg/apiserver/admission/config.go — missing
// EncryptionConfig in an active storage path is a startup error, not a warning.
// ref: go-kratos/kratos config.Watch — required dependency failure aborts boot.
func buildKeyProvider(
	storageBackend, adapterMode, providerName, masterKey, prevMasterKey string, clk clock.Clock,
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
		// Normalize hex to lowercase before demo-key check: hex.DecodeString is
		// case-insensitive, so "0123ABCD..." and "0123abcd..." produce identical
		// key material. Comparing at string level without normalization would let
		// an uppercase variant of a known demo key slip through.
		if err := rejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY", []byte(strings.ToLower(masterKey))); err != nil {
			return nil, err
		}
		if prevMasterKey != "" {
			if err := rejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", []byte(strings.ToLower(prevMasterKey))); err != nil {
				return nil, err
			}
		}
		kp, err := crypto.NewLocalAESKeyProviderFromKeys(masterKey, prevMasterKey)
		if err != nil {
			return nil, fmt.Errorf("local-aes key provider: %w", err)
		}
		slog.Info("configcore: key provider initialized", slog.String("provider", "local-aes"))
		return kp, nil
	case "vault-transit":
		kp, err := adaptervault.NewTransitKeyProviderFromEnv(isRealMode(adapterMode), clk)
		if err != nil {
			return nil, fmt.Errorf("vault-transit key provider: %w", err)
		}
		slog.Info("configcore: key provider initialized", slog.String("provider", "vault-transit"))
		return kp, nil
	default:
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_CONFIGCORE_KEY_PROVIDER; known values: \"local-aes\", \"vault-transit\"",
			errcode.WithDetails(slog.String("provider", providerName)))
	}
}

// keyProviderToTransformer wraps a KeyProvider in a ValueTransformer.
// When kp is nil or the no-key sentinel (no encryption configured), returns
// NoopTransformer.
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
