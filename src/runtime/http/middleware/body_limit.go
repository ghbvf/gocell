package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// DefaultBodyLimit is the default maximum request body size (1 MB).
const DefaultBodyLimit int64 = 1 << 20

// BodyLimit restricts the request body to at most maxBytes bytes.
// If the body exceeds the limit, a 413 JSON error response is returned.
// Pass 0 or a negative value to use DefaultBodyLimit.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultBodyLimit
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				writeBodyTooLarge(w)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func writeBodyTooLarge(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE", "request body too large")
}
