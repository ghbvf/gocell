// secrets.go: secret / key / cursor codec 加载。
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// loadSecret loads a secret from the given environment variable. In "real"
// adapter mode, the env var is required and missing values are a hard error.
// In dev mode, missing values fall back to devDefault with a warning.
//
// ref: Kubernetes two-phase validation — Complete then Validate, both fail-fast.
func loadSecret(envKey, devDefault, adapterMode string) ([]byte, error) {
	if v := os.Getenv(envKey); v != "" {
		return []byte(v), nil
	}
	if adapterMode == "real" {
		return nil, fmt.Errorf("%s must be set in adapter mode \"real\"", envKey)
	}
	slog.Warn("using dev-only default; set env var for production",
		slog.String("var", envKey),
		slog.String("mode", "dev-fallback"),
		slog.String("action_required", "set env var before real mode"))
	return []byte(devDefault), nil
}

// loadKeySet returns a KeySet, preferring environment-provided keys.
// In "real" adapter mode, env keys are required (fail-fast if missing).
// In dev mode, env keys are used if available; otherwise an ephemeral RSA
// key pair is generated per process (tokens invalidated on restart).
//
// ref: Kubernetes kube-apiserver refuses to start without --service-account-key-file.
func loadKeySet(adapterMode string) (*auth.KeySet, error) {
	// Prefer env-provided keys regardless of adapter mode.
	ks, err := auth.LoadKeySetFromEnv()
	if err == nil {
		slog.Info("JWT key set loaded from environment variables")
		return ks, nil
	}
	if adapterMode == "real" {
		return nil, fmt.Errorf("real adapter mode requires JWT key env vars: %w", err)
	}
	// Dev mode: ephemeral keys (acceptable for development only).
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey)
}

// loadCursorCodec loads a cursor HMAC secret from envName (with a dev-only
// fallback to devDefault) and constructs a CursorCodec. In "real" adapter
// mode the secret must be set and must not match a well-known demo value.
//
// When prevEnvName is non-empty and that env var is set, the value is loaded
// as the previous (verification-only) key to enable the kube-apiserver-style
// rotation lifecycle: decode tries current first, then previous. The previous
// key is subject to the same demo-key guard as current; failures at any stage
// are fail-fast (no silent fallback to single-key mode). If the previous env
// is unset, the codec is constructed in single-key mode.
//
// label is used in wrapping error messages.
//
// ref: kube-apiserver --service-account-signing-key-file (single current) +
// --service-account-key-file (multi verification) — same signing/verification
// split applied to cursor HMAC tokens.
// ref: gorilla/securecookie CodecsFromPairs — ordered key list, first match
// wins during decode.
func loadCursorCodec(adapterMode, envName, prevEnvName, devDefault, label string) (*query.CursorCodec, error) {
	key, err := loadSecret(envName, devDefault, adapterMode)
	if err != nil {
		return nil, fmt.Errorf("%s cursor key: %w", label, err)
	}
	if err := rejectDemoKey(adapterMode, envName, key); err != nil {
		return nil, err
	}

	var prevKey []byte
	if prevEnvName != "" {
		if v := os.Getenv(prevEnvName); v != "" {
			prevKey = []byte(v)
			if err := rejectDemoKey(adapterMode, prevEnvName, prevKey); err != nil {
				return nil, err
			}
		}
	}

	codec, err := query.NewCursorCodec(key, prevKey)
	if err != nil {
		return nil, fmt.Errorf("create %s cursor codec: %w", label, err)
	}
	if len(prevKey) > 0 {
		slog.Info("cursor key rotation active",
			slog.String("label", label),
			slog.String("current_env", envName),
			slog.String("previous_env", prevEnvName))
	}
	return codec, nil
}
