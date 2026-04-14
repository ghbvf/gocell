package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// ServiceTokenMaxAge is the maximum age of a service token before it is
// rejected. Tokens with timestamps at or beyond this window are refused.
const ServiceTokenMaxAge = 5 * time.Minute

// nowFunc is overridable for testing.
var nowFunc = time.Now

// MinHMACKeyBytes is the minimum HMAC secret length. NIST recommends 256-bit
// (32-byte) keys for HMAC-SHA256; this constant enforces that minimum.
const MinHMACKeyBytes = 32

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
//	ServiceToken {unix_timestamp}:{hex_hmac}
//
// The HMAC is computed over "{method} {path} {timestamp}" using secrets from
// the key ring. Verification tries each secret in order (current, then previous).
// Tokens older than 5 minutes (exclusive boundary) are rejected.
func ServiceTokenMiddleware(ring *HMACKeyRing) func(http.Handler) http.Handler {
	if ring == nil {
		// Fail-fast: refuse to create middleware with nil key ring.
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				slog.Error("service token middleware called with nil key ring")
				httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractServiceToken(r)
			if token == "" {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing service token")
				return
			}

			// Parse "{timestamp}:{signature}".
			parts := strings.SplitN(token, ":", 2)
			if len(parts) != 2 {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token format")
				return
			}

			tsStr, sigHex := parts[0], parts[1]
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token timestamp")
				return
			}

			// Check timestamp is within the allowed window.
			// Boundary (exactly 5 minutes) is rejected (>=).
			now := nowFunc()
			tokenTime := time.Unix(ts, 0)
			age := now.Sub(tokenTime)
			if age < 0 {
				age = -age
			}
			if age >= ServiceTokenMaxAge {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "service token expired")
				return
			}

			providedMAC, err := hex.DecodeString(sigHex)
			if err != nil {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token format")
				return
			}

			// Try all secrets in the key ring (current first, then previous).
			message := fmt.Sprintf("%s %s %s", r.Method, r.URL.Path, tsStr)
			for _, secret := range ring.Secrets() {
				mac := hmac.New(sha256.New, secret)
				_, _ = mac.Write([]byte(message))
				if hmac.Equal(providedMAC, mac.Sum(nil)) {
					next.ServeHTTP(w, r)
					return
				}
			}

			httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token")
		})
	}
}

// GenerateServiceToken creates a service token for the given method, path,
// and timestamp using the current secret from the key ring.
// It returns an empty string if ring is nil.
func GenerateServiceToken(ring *HMACKeyRing, method, path string, ts time.Time) string {
	if ring == nil {
		return ""
	}
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, ring.Current())
	_, _ = mac.Write([]byte(fmt.Sprintf("%s %s %s", method, path, tsStr)))
	return tsStr + ":" + hex.EncodeToString(mac.Sum(nil))
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
