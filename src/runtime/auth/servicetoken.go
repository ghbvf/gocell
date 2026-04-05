package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// ServiceTokenMiddleware validates requests using HMAC-SHA256 service tokens.
// The token is expected in the Authorization header as "ServiceToken <hex-encoded-hmac>".
// The HMAC is computed over the request method and path using the shared secret.
func ServiceTokenMiddleware(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractServiceToken(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing service token")
				return
			}

			providedMAC, err := hex.DecodeString(token)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token format")
				return
			}

			// Compute expected HMAC over "METHOD PATH".
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write([]byte(r.Method + " " + r.URL.Path))
			expectedMAC := mac.Sum(nil)

			if !hmac.Equal(providedMAC, expectedMAC) {
				writeAuthError(w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid service token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
