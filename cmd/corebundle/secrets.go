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

// buildCursorCodec constructs a CursorCodec from already-read primary and
// previous key strings. This is the low-level builder used by modules after
// they read env via LoadCursorKeys; it does not call os.Getenv itself.
//
// primary must be non-empty in "real" adapter mode (enforced via loadSecret
// semantics: callers pass "" for primary when the env var was unset, which
// triggers the real-mode fail-fast). previous may be empty (single-key mode).
//
// envLabelForErr and prevEnvLabelForErr are used only in error messages and
// the slog rotation log so operators can identify which env var to check.
// devDefault is used when primary is empty in dev mode.
//
// ref: kube-apiserver --service-account-signing-key-file (single current) +
// --service-account-key-file (multi verification) — same signing/verification
// split applied to cursor HMAC tokens.
// ref: gorilla/securecookie CodecsFromPairs — ordered key list, first match
// wins during decode.
func buildCursorCodec(adapterMode, envLabelForErr, prevEnvLabelForErr, primary, previous, devDefault, label string) (*query.CursorCodec, error) {
	var key []byte
	if primary != "" {
		key = []byte(primary)
	} else if adapterMode == "real" {
		return nil, fmt.Errorf("%s cursor key: %s must be set in adapter mode \"real\"", label, envLabelForErr)
	} else {
		slog.Warn("using dev-only default; set env var for production",
			slog.String("var", envLabelForErr),
			slog.String("mode", "dev-fallback"),
			slog.String("action_required", "set env var before real mode"))
		key = []byte(devDefault)
	}
	if err := rejectDemoKey(adapterMode, envLabelForErr, key); err != nil {
		return nil, err
	}

	var prevKey []byte
	if previous != "" {
		prevKey = []byte(previous)
		if err := rejectDemoKey(adapterMode, prevEnvLabelForErr, prevKey); err != nil {
			return nil, err
		}
	}

	codec, err := query.NewCursorCodec(key, prevKey)
	if err != nil {
		return nil, fmt.Errorf("create %s cursor codec: %w", label, err)
	}
	if len(prevKey) > 0 {
		slog.Info("cursor key rotation active",
			slog.String("label", label),
			slog.String("current_env", envLabelForErr),
			slog.String("previous_env", prevEnvLabelForErr))
	}
	return codec, nil
}

// loadCursorCodec loads a cursor HMAC secret from envName (with a dev-only
// fallback to devDefault) and constructs a CursorCodec. In "real" adapter
// mode the secret must be set and must not match a well-known demo value.
//
// Deprecated: prefer LoadCursorKeys + buildCursorCodec which separates env
// reading from codec construction. This wrapper is retained for call sites
// that have not yet migrated.
func loadCursorCodec(adapterMode, envName, prevEnvName, devDefault, label string) (*query.CursorCodec, error) {
	primary, previous := os.Getenv(envName), ""
	if prevEnvName != "" {
		previous = os.Getenv(prevEnvName)
	}
	return buildCursorCodec(adapterMode, envName, prevEnvName, primary, previous, devDefault, label)
}
