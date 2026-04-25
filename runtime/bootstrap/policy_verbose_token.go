package bootstrap

// policy_verbose_token.go — PolicyVerboseToken: verbose-mode access guard.
//
// Absorbs PR-A35 READYZ-VERBOSE-TOKEN-DENY-01:
// when the ?verbose query parameter is present, the request must supply a
// matching token in a configured header; mismatch returns 401 JSON.
// Requests without ?verbose pass through unconditionally.
//
// ARCH-04: PolicyVerboseToken is intended for the /readyz sub-group only.
// Applying it to a broader mux works but is semantically narrower than the
// name implies. Document clearly: mount this policy only on the route group
// or sub-router that serves /readyz; do not apply it at listener level unless
// all endpoints share the verbose-token guard semantics.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/health/probequery"
)

// verboseTokenErrorBody is the canonical 401 response body for a token mismatch.
// Pre-encoded once to avoid per-request JSON marshalling overhead.
var verboseTokenErrorBody = mustEncodeVerboseTokenError()

func mustEncodeVerboseTokenError() []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "ERR_AUTH_VERBOSE_TOKEN",
			"message": "verbose token mismatch",
		},
	})
	if err != nil {
		// json.Marshal of a static literal cannot fail in practice.
		panic("bootstrap: failed to pre-encode verbose token error body: " + err.Error())
	}
	return body
}

// verboseTokenMiddleware creates an HTTP middleware that guards ?verbose mode
// with a shared secret supplied in a request header.
func verboseTokenMiddleware(headerName, token string) func(http.Handler) http.Handler {
	// Hash the configured token once so each request only needs to hash the
	// submitted header. Comparing fixed-length 32-byte digests via
	// subtle.ConstantTimeCompare avoids the length-oracle that bare-bytes
	// ConstantTimeCompare exhibits (it short-circuits on length mismatch,
	// leaking the configured token's length via timing). Same model as
	// runtime/http/health.verboseDecision and cmd/corebundle/metrics.go.
	configuredHash := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SEC-06: only enforce when ?verbose is semantically "on" — same
			// parser as the health handler so policy and handler agree on what
			// counts as verbose. (?verbose=false / ?verbose=0 must not 401, e.g.
			// kubelet probes that pass them through.)
			if !probequery.Verbose(r) {
				next.ServeHTTP(w, r)
				return
			}
			submittedHash := sha256.Sum256([]byte(r.Header.Get(headerName)))
			if subtle.ConstantTimeCompare(submittedHash[:], configuredHash[:]) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write(verboseTokenErrorBody)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PolicyVerboseToken returns a cell.Policy that guards ?verbose query-param
// access with a shared secret supplied in a request header.
//
// When the ?verbose query parameter is absent the middleware is a pass-through.
// When ?verbose is present:
//   - If the headerName header matches token exactly → pass through.
//   - Otherwise → 401 {"error":{"code":"ERR_AUTH_VERBOSE_TOKEN","message":"verbose token mismatch"}}.
//
// Fail-fast:
//   - headerName empty → panic "bootstrap: PolicyVerboseToken headerName must not be empty"
//   - token empty      → panic "bootstrap: PolicyVerboseToken token must not be empty"
//
// This absorbs PR-A35 READYZ-VERBOSE-TOKEN-DENY-01.
func PolicyVerboseToken(headerName, token string) cell.Policy {
	if headerName == "" {
		panic("bootstrap: PolicyVerboseToken headerName must not be empty")
	}
	if token == "" {
		panic("bootstrap: PolicyVerboseToken token must not be empty")
	}
	return cell.Policy{
		Name:       "verbose-token",
		Middleware: verboseTokenMiddleware(headerName, token),
	}
}
