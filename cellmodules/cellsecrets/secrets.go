// Package cellsecrets provides helpers shared across the platform/ cell
// modules (accesscore, auditcore, configcore) and cmd/corebundle.
//
// Unlike platform/internal/cellsecrets, this package is importable by
// cmd/corebundle (composition root). It contains helpers that both platform/
// cell modules and cmd/ need: demo-key rejection, cursor codec construction,
// HMAC key loading, JWT key set loading, and the pgxpool type assertion.
package cellsecrets

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/capability"
)

// RealAdapterMode is the canonical value activating production fail-fast behavior.
const RealAdapterMode = "real"

// IsRealMode reports whether adapterMode activates real-mode guards.
func IsRealMode(adapterMode string) bool {
	return adapterMode == RealAdapterMode
}

// WellKnownDemoKeys is the append-only list of key material shipped as public
// dev defaults. Real-mode startup must refuse any of these values.
//
// DO NOT COPY TO PRODUCTION — these values are public in git history.
var WellKnownDemoKeys = []string{
	"dev-hmac-key-replace-in-prod!!!!",
	"dev-hmac-bootstrap-replace-32b!!",
	"gocell-demo-AUDIT--CORE-key-32!!",
	"gocell-demo-CONFIG-CORE-key-32!!",
	"gocell-demo-ORDER-CELL-key-32b!!",
	"gocell-demo-DEVICE-CELL-key-32!!",
	"gocell-demo-ACCESS-CORE-key-32!!",
	"corebundle-audit-cursor-key-32b!",
	"corebundle-cfg-cursor-key--32bb!",
	"corebundle-access-cursor-key32!!",
	"service-secret-32-bytes-xxxxxx!!",
	"walkthrough-service-token-secret-32b",
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	"l2-test-secret-32-bytes-padding!!",
	"l2-test-hmac-key-32-bytes-pad!!!",
	"l2-audit-cursor-key-32-bytes!!!!",
	"l2-config-cursor-key-32-bytes!!!",
	// starter-dev-secret-32-bytes-ok!! is the hardcoded HMAC secret used by
	// examples/corebundlestarter (devServiceSecret const). Must be caught by
	// RejectDemoKey to prevent accidental copy to a real GOCELL_SERVICE_SECRET.
	//
	// #nosec G101 -- known public demo value; presence here is the security mechanism.
	"starter-dev-secret-32-bytes-ok!!",
}

// RejectDemoKey returns an error in real mode when key matches a well-known
// demo value.  The comparison is performed with crypto/subtle.ConstantTimeCompare
// to avoid timing oracles (startup, not hot-path).
func RejectDemoKey(adapterMode, envName string, key []byte) error {
	if !IsRealMode(adapterMode) {
		return nil
	}
	for _, demo := range WellKnownDemoKeys {
		if subtle.ConstantTimeCompare(key, []byte(demo)) == 1 {
			return fmt.Errorf("%s is set to a well-known demo key; "+
				"rotate to a fresh random 32-byte secret before running in real adapter mode", envName)
		}
	}
	return nil
}

// ResolveAndRejectDemoKey picks key material from primary (or falls back to
// devDefault in dev mode), fails fast in real mode when primary is empty, and
// rejects known-demo key values.
func ResolveAndRejectDemoKey(adapterMode, envLabel, primary, devDefault string) ([]byte, error) {
	var key []byte
	switch {
	case primary != "":
		key = []byte(primary)
	case IsRealMode(adapterMode):
		return nil, fmt.Errorf("%s must be set in adapter mode \"real\"", envLabel)
	default:
		slog.Warn("using dev-only default; set env var for production",
			slog.String("var", envLabel),
			slog.String("mode", "dev-fallback"),
			slog.String("action_required", "set env var before real mode"))
		key = []byte(devDefault)
	}
	if err := RejectDemoKey(adapterMode, envLabel, key); err != nil {
		return nil, err
	}
	return key, nil
}

// CursorCodecConfig encapsulates BuildCursorCodec parameters.
type CursorCodecConfig struct {
	AdapterMode string
	EnvName     string
	PrevEnvName string
	Primary     string
	Previous    string
	DevDefault  string
	Label       string
}

// BuildCursorCodec constructs a CursorCodec from already-read primary and
// previous key strings.
func BuildCursorCodec(cfg CursorCodecConfig) (*query.CursorCodec, error) {
	key, err := ResolveAndRejectDemoKey(cfg.AdapterMode, cfg.EnvName, cfg.Primary, cfg.DevDefault)
	if err != nil {
		return nil, fmt.Errorf("%s cursor key: %w", cfg.Label, err)
	}

	var prevKey []byte
	if cfg.Previous != "" {
		prevKey = []byte(cfg.Previous)
		if err := RejectDemoKey(cfg.AdapterMode, cfg.PrevEnvName, prevKey); err != nil {
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

// HMACKeyConfig encapsulates BuildHMACKey parameters.
type HMACKeyConfig struct {
	AdapterMode string
	EnvName     string
	Primary     string
	DevDefault  string
}

// BuildHMACKey loads and validates the HMAC key.
func BuildHMACKey(cfg HMACKeyConfig) ([]byte, error) {
	return ResolveAndRejectDemoKey(cfg.AdapterMode, cfg.EnvName, cfg.Primary, cfg.DevDefault)
}

// InternalAddrToBaseURL converts a bind address to an HTTP base URL for the
// internal HTTP client. Mirrors cmd/corebundle.internalAddrToBaseURL.
func InternalAddrToBaseURL(addr string) string {
	if addr == "" {
		return "http://127.0.0.1:9090"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	if after, ok := strings.CutPrefix(addr, "0.0.0.0:"); ok {
		return "http://127.0.0.1:" + after
	}
	return "http://" + addr
}

// LoadKeySet returns a KeySet, preferring environment-provided keys.
// In "real" adapter mode, env keys are required (fail-fast if missing).
func LoadKeySet(adapterMode string, clk clock.Clock) (*auth.KeySet, error) {
	ks, err := auth.LoadKeySetFromEnv(clk)
	if err == nil {
		slog.Info("JWT key set loaded from environment variables")
		return ks, nil
	}
	if IsRealMode(adapterMode) {
		return nil, fmt.Errorf("real adapter mode requires JWT key env vars: %w", err)
	}
	privKey, pubKey, genErr := auth.GenerateRSAKeyPair()
	if genErr != nil {
		return nil, fmt.Errorf("dev mode ephemeral key pair: %w", genErr)
	}
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey, clk)
}

// PgxPoolFromProvider is the single type-assertion site for the
// capability.PGProvider's any-typed DB() seam. Placed here so all platform
// cell modules and cmd/ reuse it without duplicating the assertion.
//
// platform/cellsecrets is a composition-root layer (like cmd/) so
// importing adapters/ (pgxpool) is architecturally acceptable.
func PgxPoolFromProvider(pg capability.PGProvider) (*pgxpool.Pool, error) {
	pool, ok := pg.DB().(*pgxpool.Pool)
	if !ok {
		return nil, fmt.Errorf("platform: PG provider DB() is not *pgxpool.Pool (got %T)", pg.DB())
	}
	return pool, nil
}
