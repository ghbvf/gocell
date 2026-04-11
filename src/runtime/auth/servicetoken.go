package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// ServiceTokenMaxAge is the maximum age of a service token before it is
// rejected. Tokens with timestamps at or beyond this window are refused.
const ServiceTokenMaxAge = 5 * time.Minute

// nowFunc is overridable for testing.
var nowFunc = time.Now

// ServiceTokenMiddleware validates requests using HMAC-SHA256 service tokens.
// The token is expected in the Authorization header as:
//
//	ServiceToken {unix_timestamp}:{hex_hmac}
//
// The HMAC is computed over "{method} {path} {timestamp}" using the shared
// secret. Tokens older than 5 minutes (exclusive boundary) are rejected.
func ServiceTokenMiddleware(secret []byte) func(http.Handler) http.Handler {
	if len(secret) == 0 {
		// Fail-fast: refuse to create middleware with empty secret.
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				slog.Error("service token middleware called with empty secret")
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

			// Compute expected HMAC over "METHOD PATH TIMESTAMP".
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write([]byte(fmt.Sprintf("%s %s %s", r.Method, r.URL.Path, tsStr)))
			expectedMAC := mac.Sum(nil)

			if !hmac.Equal(providedMAC, expectedMAC) {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// GenerateServiceToken creates a service token for the given method, path,
// and timestamp using the shared secret.
func GenerateServiceToken(secret []byte, method, path string, ts time.Time) string {
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
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
