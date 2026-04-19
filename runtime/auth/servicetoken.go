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

const msgInvalidServiceTokenFormat = "invalid service token format"

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
//
// Both variables are read as raw UTF-8 strings and used directly as HMAC key
// bytes (no base64 decoding is performed). The value must be at least 32 bytes
// long. To generate a suitable value: openssl rand -base64 32
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
//
// Principal construction is fully delegated to NewServiceTokenAuthenticator
// so that the service identity shape is defined in a single place.
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

	// Construct a single Authenticator instance for the lifetime of this
	// middleware. All validation and Principal construction is delegated here,
	// eliminating the previously duplicated logic in handleServiceToken.
	auth := NewServiceTokenAuthenticator(ring, opts...)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleServiceToken(cfg, auth, next, w, r)
		})
	}
}

// handleServiceToken contains all request-handling logic extracted from
// ServiceTokenMiddleware to reduce cognitive complexity.
// Validation and Principal construction are fully delegated to the provided
// Authenticator (NewServiceTokenAuthenticator); this function maps the returned
// error to granular metrics labels and HTTP responses, preserving all existing
// metric reason labels and HTTP status codes.
func handleServiceToken(cfg serviceTokenConfig, auth Authenticator, next http.Handler, w http.ResponseWriter, r *http.Request) {
	token := extractServiceToken(r)
	if token == "" {
		cfg.metrics.recordServiceVerify("failure", "missing")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing service token")
		return
	}

	// Log legacy 2-part format before validation so ops can observe the traffic.
	if parts := strings.SplitN(token, ":", 3); len(parts) == 2 {
		cfg.logger.WarnContext(r.Context(), "legacy service token format rejected",
			slog.String("path", r.URL.Path),
			slog.String("format", "2-part"),
		)
	}

	p, ok, err := auth.Authenticate(r)
	if err != nil {
		writeServiceTokenError(cfg, err, w, r)
		return
	}
	if !ok {
		// Absent: no ServiceToken header (already handled by extractServiceToken
		// above, so this branch is a safety net for unexpected absent outcomes).
		cfg.metrics.recordServiceVerify("failure", "missing")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing service token")
		return
	}

	cfg.metrics.recordServiceVerify("success", "ok")
	ctx := WithPrincipal(r.Context(), p)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// writeServiceTokenError maps a verifyServiceTokenPayload error to granular
// metrics labels and the appropriate HTTP response. It preserves the
// middleware's split between 401 (auth failures, replay) and 500 (nonce store
// infrastructure failures) by inspecting the wrapped Cause.
func writeServiceTokenError(cfg serviceTokenConfig, err error, w http.ResponseWriter, r *http.Request) {
	// errors.Is traverses the full chain, so ErrNonceReused in the Cause matches.
	if errors.Is(err, ErrNonceReused) {
		cfg.metrics.recordServiceVerify("failure", "replay")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "service token replay detected")
		return
	}

	// If the error has a Cause (wrapped by WrapAuth for nonce check failures)
	// that is NOT ErrNonceReused, it is a nonce store infrastructure failure.
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Cause != nil {
		cfg.metrics.recordServiceVerify("failure", "nonce_store_error")
		cfg.logger.Error("nonce store check failed", slog.Any("error", ec.Cause))
		httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
		return
	}

	// Classify remaining auth errors by their message for metric granularity.
	reason := classifyServiceTokenVerifyError(err)
	cfg.metrics.recordServiceVerify("failure", reason)
	httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token")
}

// classifyServiceTokenVerifyError maps a verifyServiceTokenPayload error to a
// metric reason label. This mirrors the legacy per-branch labels from the
// original handleServiceToken implementation.
func classifyServiceTokenVerifyError(err error) string {
	if err == nil {
		return "ok"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "legacy 2-part"):
		return "legacy_format"
	case strings.Contains(msg, "expired"):
		return "expired"
	case strings.Contains(msg, "invalid service token MAC"):
		return "invalid_mac"
	default:
		return "invalid_format"
	}
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
