// secrets.go: secret / key / cursor codec 加载。
package main

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// loadKeySet returns a KeySet, preferring environment-provided keys.
// In "real" adapter mode, env keys are required (fail-fast if missing).
// In dev mode, env keys are used if available; otherwise an ephemeral RSA
// key pair is generated per process (tokens invalidated on restart).
//
// ref: Kubernetes kube-apiserver refuses to start without --service-account-key-file.
func loadKeySet(adapterMode string, clk clock.Clock) (*auth.KeySet, error) {
	// Prefer env-provided keys regardless of adapter mode.
	ks, err := auth.LoadKeySetFromEnv(clk)
	if err == nil {
		slog.Info("JWT key set loaded from environment variables")
		return ks, nil
	}
	if isRealMode(adapterMode) {
		return nil, fmt.Errorf("real adapter mode requires JWT key env vars: %w", err)
	}
	// Dev mode: ephemeral keys (acceptable for development only).
	privKey, pubKey, err := auth.GenerateRSAKeyPair()
	if err != nil {
		return nil, fmt.Errorf("dev mode ephemeral key pair: %w", err)
	}
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey, clk)
}

// cursorCodecConfig encapsulates buildCursorCodec parameters to avoid passing
// 7 positional string arguments at every call site.
type cursorCodecConfig struct {
	AdapterMode string
	EnvName     string // primary env variable name, used in error messages / slog
	PrevEnvName string // previous env variable name, used in error messages / slog
	Primary     string
	Previous    string
	DevDefault  string
	Label       string // "audit" / "config" / "access"
}

// resolveAndRejectDemoKey picks key material from the provided primary (or
// falls back to devDefault in dev mode), fails fast in real mode when primary
// is empty, and rejects known-demo key values. It is the shared prefix used by
// buildCursorCodec and buildHMACKey — extracting it keeps the two public
// builders slim and avoids SonarCloud duplication complaints on their
// prologues.
//
// ref: go-zero core/service/serviceconf.go — strict mode rejects insecure
// defaults uniformly across subsystems.
func resolveAndRejectDemoKey(adapterMode, envLabel, primary, devDefault string) ([]byte, error) {
	var key []byte
	switch {
	case primary != "":
		key = []byte(primary)
	case isRealMode(adapterMode):
		return nil, fmt.Errorf("%s must be set in adapter mode \"real\"", envLabel)
	default:
		slog.Warn("using dev-only default; set env var for production",
			slog.String("var", envLabel),
			slog.String("mode", "dev-fallback"),
			slog.String("action_required", "set env var before real mode"))
		key = []byte(devDefault)
	}
	if err := rejectDemoKey(adapterMode, envLabel, key); err != nil {
		return nil, err
	}
	return key, nil
}

// buildCursorCodec constructs a CursorCodec from already-read primary and
// previous key strings. This is the low-level builder used by modules after
// they read env via LoadCursorKeys; it does not call os.Getenv itself.
//
// Primary must be non-empty in "real" adapter mode (enforced via
// resolveAndRejectDemoKey: callers pass "" for primary when the env var was
// unset, which triggers the real-mode fail-fast). Previous may be empty
// (single-key mode).
//
// cfg.EnvName and cfg.PrevEnvName are used only in error messages and the
// slog rotation log so operators can identify which env var to check.
// cfg.DevDefault is used when primary is empty in dev mode.
//
// ref: kube-apiserver --service-account-signing-key-file (single current) +
// --service-account-key-file (multi verification) — same signing/verification
// split applied to cursor HMAC tokens.
// ref: gorilla/securecookie CodecsFromPairs — ordered key list, first match
// wins during decode.
func buildCursorCodec(cfg cursorCodecConfig) (*query.CursorCodec, error) {
	key, err := resolveAndRejectDemoKey(cfg.AdapterMode, cfg.EnvName, cfg.Primary, cfg.DevDefault)
	if err != nil {
		return nil, fmt.Errorf("%s cursor key: %w", cfg.Label, err)
	}

	var prevKey []byte
	if cfg.Previous != "" {
		prevKey = []byte(cfg.Previous)
		if err := rejectDemoKey(cfg.AdapterMode, cfg.PrevEnvName, prevKey); err != nil {
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
			slog.String("current_env", cfg.EnvName),
			slog.String("previous_env", cfg.PrevEnvName))
	}
	return codec, nil
}

// hmacKeyConfig encapsulates buildHMACKey parameters.
type hmacKeyConfig struct {
	AdapterMode string
	EnvName     string // env variable name used in error messages
	Primary     string // provided by LoadCellHMACKey; empty → dev fallback
	DevDefault  string // dev mode fallback value
}

// buildHMACKey loads and validates the HMAC key by delegating to
// resolveAndRejectDemoKey. Logic:
//   - primary non-empty: use directly
//   - primary empty + real mode: fail-fast with envName in error message
//   - primary empty + dev mode: use DevDefault + slog.Warn
//   - all paths: rejectDemoKey applied before returning
//
// The error message contains only the env variable name — no module label.
// Module callers (e.g. audit_module.go) wrap with their own context label.
//
// ref: Kratos config/env prefix-strip convention — each module reads its own namespace.
func buildHMACKey(cfg hmacKeyConfig) ([]byte, error) {
	return resolveAndRejectDemoKey(cfg.AdapterMode, cfg.EnvName, cfg.Primary, cfg.DevDefault)
}
