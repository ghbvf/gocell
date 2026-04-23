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
	if isRealMode(adapterMode) {
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
	if isRealMode(adapterMode) {
		return nil, fmt.Errorf("real adapter mode requires JWT key env vars: %w", err)
	}
	// Dev mode: ephemeral keys (acceptable for development only).
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey)
}

// cursorCodecConfig encapsulates buildCursorCodec parameters to avoid passing
// 7 positional string arguments at every call site.
type cursorCodecConfig struct {
	AdapterMode  string
	EnvLabel     string // primary env name, used only in error messages / slog
	PrevEnvLabel string // previous env name, used only in error messages / slog
	Primary      string
	Previous     string
	DevDefault   string
	Label        string // "audit" / "config" / "access"
}

// buildCursorCodec constructs a CursorCodec from already-read primary and
// previous key strings. This is the low-level builder used by modules after
// they read env via LoadCursorKeys; it does not call os.Getenv itself.
//
// Primary must be non-empty in "real" adapter mode (enforced via loadSecret
// semantics: callers pass "" for primary when the env var was unset, which
// triggers the real-mode fail-fast). Previous may be empty (single-key mode).
//
// cfg.EnvLabel and cfg.PrevEnvLabel are used only in error messages and the
// slog rotation log so operators can identify which env var to check.
// cfg.DevDefault is used when primary is empty in dev mode.
//
// ref: kube-apiserver --service-account-signing-key-file (single current) +
// --service-account-key-file (multi verification) — same signing/verification
// split applied to cursor HMAC tokens.
// ref: gorilla/securecookie CodecsFromPairs — ordered key list, first match
// wins during decode.
func buildCursorCodec(cfg cursorCodecConfig) (*query.CursorCodec, error) {
	var key []byte
	if cfg.Primary != "" {
		key = []byte(cfg.Primary)
	} else if isRealMode(cfg.AdapterMode) {
		return nil, fmt.Errorf("%s cursor key: %s must be set in adapter mode \"real\"", cfg.Label, cfg.EnvLabel)
	} else {
		slog.Warn("using dev-only default; set env var for production",
			slog.String("var", cfg.EnvLabel),
			slog.String("mode", "dev-fallback"),
			slog.String("action_required", "set env var before real mode"))
		key = []byte(cfg.DevDefault)
	}
	if err := rejectDemoKey(cfg.AdapterMode, cfg.EnvLabel, key); err != nil {
		return nil, err
	}

	var prevKey []byte
	if cfg.Previous != "" {
		prevKey = []byte(cfg.Previous)
		if err := rejectDemoKey(cfg.AdapterMode, cfg.PrevEnvLabel, prevKey); err != nil {
			return nil, err
		}
	}

	codec, err := query.NewCursorCodec(key, prevKey)
	if err != nil {
		return nil, fmt.Errorf("create %s cursor codec: %w", cfg.Label, err)
	}
	if len(prevKey) > 0 {
		slog.Info("cursor key rotation active",
			slog.String("label", cfg.Label),
			slog.String("current_env", cfg.EnvLabel),
			slog.String("previous_env", cfg.PrevEnvLabel))
	}
	return codec, nil
}

// hmacKeyConfig encapsulates buildHMACKey parameters.
type hmacKeyConfig struct {
	AdapterMode string
	EnvLabel    string // used in error messages
	Primary     string // provided by LoadCellHMACKey; empty → dev fallback
	DevDefault  string // dev mode fallback value
	Label       string // "auditcore HMAC" etc., used in error wrapping
}

// buildHMACKey loads and validates the HMAC key. Logic mirrors buildCursorCodec:
//   - primary non-empty: use directly
//   - primary empty + real mode: fail-fast
//   - primary empty + dev mode: use DevDefault + slog.Warn
//   - all paths: rejectDemoKey applied before returning
//
// ref: Kratos config/env prefix-strip convention — each module reads its own namespace.
func buildHMACKey(cfg hmacKeyConfig) ([]byte, error) {
	var key []byte
	if cfg.Primary != "" {
		key = []byte(cfg.Primary)
	} else if isRealMode(cfg.AdapterMode) {
		return nil, fmt.Errorf("%s: %s must be set in adapter mode \"real\"", cfg.Label, cfg.EnvLabel)
	} else {
		slog.Warn("using dev-only default; set env var for production",
			slog.String("var", cfg.EnvLabel),
			slog.String("mode", "dev-fallback"),
			slog.String("action_required", "set env var before real mode"))
		key = []byte(cfg.DevDefault)
	}
	if err := rejectDemoKey(cfg.AdapterMode, cfg.EnvLabel, key); err != nil {
		return nil, err
	}
	return key, nil
}
