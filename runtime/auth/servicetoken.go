package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// WithServiceTokenLogger sets the logger for ServiceTokenMiddleware.
func WithServiceTokenLogger(l *slog.Logger) ServiceTokenOption {
	return func(c *serviceTokenConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// ServiceTokenMaxAge is the maximum age of a service token before it is
// rejected. Tokens with timestamps at or beyond this window are refused.
const ServiceTokenMaxAge = 5 * time.Minute

// MinHMACKeyBytes is the minimum HMAC secret length. NIST recommends 256-bit
// (32-byte) keys for HMAC-SHA256; this constant enforces that minimum.
const MinHMACKeyBytes = 32

// serviceTokenConfig holds per-middleware options.
type serviceTokenConfig struct {
	now        func() time.Time
	logger     *slog.Logger
	nonceStore NonceStore
	metrics    *AuthMetrics
}

// ServiceTokenOption configures ServiceTokenMiddleware behavior.
type ServiceTokenOption func(*serviceTokenConfig)

// WithServiceTokenClock overrides the time source for timestamp validation.
func WithServiceTokenClock(fn func() time.Time) ServiceTokenOption {
	return func(c *serviceTokenConfig) {
		if fn != nil {
			c.now = fn
		}
	}
}

// WithNonceStore sets a NonceStore for replay prevention. When set, the
// middleware rejects tokens whose nonce has already been consumed within
// the NonceStore's TTL window.
func WithNonceStore(ns NonceStore) ServiceTokenOption {
	return func(c *serviceTokenConfig) { c.nonceStore = ns }
}

// WithServiceTokenMetrics sets the AuthMetrics for ServiceTokenMiddleware.
func WithServiceTokenMetrics(m *AuthMetrics) ServiceTokenOption {
	return func(c *serviceTokenConfig) { c.metrics = m }
}

// HMACKeyRing holds an ordered pair of HMAC secrets for service token operations.
// Position 0 (current) is used for signing; verification tries all secrets in order.
//
// ref: zeromicro/go-zero rest/token/tokenparser.go — dual-key [current, previous] model
// ref: gorilla/securecookie — DecodeMulti try-all-keys pattern
type HMACKeyRing struct {
	current  []byte
	previous []byte
}

// NewHMACKeyRing creates an HMACKeyRing. current must be at least MinHMACKeyBytes
// (32 bytes). previous may be nil for single-secret mode; if set, it must also
// meet the minimum length.
func NewHMACKeyRing(current []byte, previous []byte) (*HMACKeyRing, error) {
	if len(current) == 0 {
		return nil, errcode.New(errcode.ErrAuthKeyMissing, "current HMAC secret must not be empty")
	}
	if len(current) < MinHMACKeyBytes {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid,
			fmt.Sprintf("current HMAC secret is %d bytes, minimum is %d", len(current), MinHMACKeyBytes))
	}
	if len(previous) > 0 && len(previous) < MinHMACKeyBytes {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid,
			fmt.Sprintf("previous HMAC secret is %d bytes, minimum is %d", len(previous), MinHMACKeyBytes))
	}
	return &HMACKeyRing{
		current:  current,
		previous: previous,
	}, nil
}

// Current returns a copy of the active signing secret.
// The returned slice is a fresh allocation; callers cannot mutate the ring's
// internal state.
func (r *HMACKeyRing) Current() []byte {
	c := make([]byte, len(r.current))
	copy(c, r.current)
	return c
}

// Secrets returns a copy of all secrets in try-order: current first, then previous (if set).
// The returned slice is a fresh allocation; callers cannot mutate the ring's internal state.
func (r *HMACKeyRing) Secrets() [][]byte {
	if len(r.previous) == 0 {
		return [][]byte{append([]byte(nil), r.current...)}
	}
	return [][]byte{
		append([]byte(nil), r.current...),
		append([]byte(nil), r.previous...),
	}
}

const (
	// EnvServiceSecret is the environment variable for the current HMAC secret.
	EnvServiceSecret = "GOCELL_SERVICE_SECRET"
	// EnvServiceSecretPrevious is the environment variable for the previous HMAC secret.
	EnvServiceSecretPrevious = "GOCELL_SERVICE_SECRET_PREVIOUS"
)

// LoadHMACKeyRingFromEnv loads an HMACKeyRing from environment variables.
// GOCELL_SERVICE_SECRET is required; GOCELL_SERVICE_SECRET_PREVIOUS is optional.
func LoadHMACKeyRingFromEnv() (*HMACKeyRing, error) {
	current := os.Getenv(EnvServiceSecret)
	if current == "" {
		return nil, errcode.New(errcode.ErrAuthKeyMissing,
			fmt.Sprintf("environment variable %s is not set", EnvServiceSecret))
	}

	previous := os.Getenv(EnvServiceSecretPrevious)
	var prevBytes []byte
	if previous != "" {
		prevBytes = []byte(previous)
	}

	return NewHMACKeyRing([]byte(current), prevBytes)
}

// ServiceTokenMiddleware validates requests using HMAC-SHA256 service tokens.
// The token is expected in the Authorization header as:
//
//	ServiceToken {unix_timestamp}:{nonce}:{hex_hmac}
//
// The HMAC is computed over
// "{method} {path}[?{canonicalQuery}] {timestamp} {nonce}".
//
// Verification tries each secret in the key ring in order (current, then
// previous). Tokens older than 5 minutes (exclusive boundary) are rejected.
func ServiceTokenMiddleware(ring *HMACKeyRing, opts ...ServiceTokenOption) func(http.Handler) http.Handler {
	cfg := serviceTokenConfig{now: time.Now, logger: slog.Default()}
	for _, o := range opts {
		o(&cfg)
	}

	if ring == nil {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cfg.metrics.recordServiceVerify("failure", "internal")
				cfg.logger.Error("service token middleware called with nil key ring")
				httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleServiceToken(cfg, ring, next, w, r)
		})
	}
}

// handleServiceToken contains all request-handling logic extracted from
// ServiceTokenMiddleware to reduce cognitive complexity.
func handleServiceToken(cfg serviceTokenConfig, ring *HMACKeyRing, next http.Handler, w http.ResponseWriter, r *http.Request) {
	token := extractServiceToken(r)
	if token == "" {
		cfg.metrics.recordServiceVerify("failure", "missing")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing service token")
		return
	}

	parts := strings.SplitN(token, ":", 3)
	if len(parts) == 2 {
		// 2-part legacy format ({timestamp}:{hex_hmac}) — no nonce, always rejected.
		// Recorded separately so ops can observe residual legacy token traffic.
		cfg.metrics.recordServiceVerify("failure", "legacy_format")
		cfg.logger.WarnContext(r.Context(), "legacy service token format rejected",
			slog.String("path", r.URL.Path),
			slog.String("format", "2-part"),
		)
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token format")
		return
	}
	if len(parts) != 3 {
		cfg.metrics.recordServiceVerify("failure", "invalid_format")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token format")
		return
	}

	tsStr := parts[0]
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		cfg.metrics.recordServiceVerify("failure", "invalid_format")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token timestamp")
		return
	}

	now := cfg.now()
	tokenTime := time.Unix(ts, 0)
	age := now.Sub(tokenTime)
	if age < 0 {
		age = -age
	}
	if age >= ServiceTokenMaxAge {
		cfg.metrics.recordServiceVerify("failure", "expired")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "service token expired")
		return
	}

	nonce, sigHex := parts[1], parts[2]
	message := buildServiceTokenMessage(r.Method, r.URL.Path, r.URL.RawQuery, tsStr, nonce)

	providedMAC, err := hex.DecodeString(sigHex)
	if err != nil {
		cfg.metrics.recordServiceVerify("failure", "invalid_format")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token format")
		return
	}

	if !verifyServiceTokenMAC(ring, message, providedMAC) {
		cfg.metrics.recordServiceVerify("failure", "invalid_mac")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token")
		return
	}

	if blocked := checkNonceReplay(cfg, nonce, w, r); blocked {
		return
	}

	cfg.metrics.recordServiceVerify("success", "ok")
	next.ServeHTTP(w, r)
}

// checkNonceReplay handles nonce-based replay detection. Returns true if the
// request was blocked (response already written).
func checkNonceReplay(cfg serviceTokenConfig, nonce string, w http.ResponseWriter, r *http.Request) bool {
	if cfg.nonceStore == nil {
		return false
	}
	err := cfg.nonceStore.CheckAndMark(r.Context(), nonce)
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNonceReused) {
		cfg.metrics.recordServiceVerify("failure", "replay")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "service token replay detected")
	} else {
		cfg.metrics.recordServiceVerify("failure", "nonce_store_error")
		cfg.logger.Error("nonce store check failed", slog.Any("error", err))
		httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
	}
	return true
}

// verifyServiceTokenMAC checks whether the provided MAC is valid for message
// under any of the secrets in the key ring.
func verifyServiceTokenMAC(ring *HMACKeyRing, message string, providedMAC []byte) bool {
	for _, secret := range ring.Secrets() {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(message))
		if hmac.Equal(providedMAC, mac.Sum(nil)) {
			return true
		}
	}
	return false
}

// buildServiceTokenMessage constructs the canonical HMAC message for the new
// 3-part token format. The query string is canonicalized (keys sorted) and
// appended to the path when non-empty.
func buildServiceTokenMessage(method, path, rawQuery, tsStr, nonce string) string {
	cq := canonicalQuery(rawQuery)
	if cq != "" {
		return fmt.Sprintf("%s %s?%s %s %s", method, path, cq, tsStr, nonce)
	}
	return fmt.Sprintf("%s %s %s %s", method, path, tsStr, nonce)
}

// canonicalQuery returns a deterministic encoding of rawQuery with keys sorted.
// Returns empty string when rawQuery is empty. Falls back to rawQuery if
// url.ParseQuery fails.
func canonicalQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	return params.Encode() // url.Values.Encode() sorts keys alphabetically
}

// GenerateServiceToken creates a service token for the given method, path,
// optional rawQuery, and timestamp using the current secret from the key ring.
// The token format is "{timestamp}:{nonce}:{hex_hmac}" where nonce is 16
// cryptographically random bytes, hex-encoded. It returns an empty string if
// ring is nil.
func GenerateServiceToken(ring *HMACKeyRing, method, path, rawQuery string, ts time.Time) string {
	if ring == nil {
		return ""
	}
	tsStr := strconv.FormatInt(ts.Unix(), 10)

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		// crypto/rand failure is not recoverable; return empty to signal error.
		return ""
	}
	nonce := hex.EncodeToString(nonceBytes)

	message := buildServiceTokenMessage(method, path, rawQuery, tsStr, nonce)
	mac := hmac.New(sha256.New, ring.Current())
	_, _ = mac.Write([]byte(message))
	return tsStr + ":" + nonce + ":" + hex.EncodeToString(mac.Sum(nil))
}

func extractServiceToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "servicetoken") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
