package bootstrap

// policy_verbose_token.go — PolicyVerboseToken: verbose-mode access guard.
//
// Absorbs PR-A35 READYZ-VERBOSE-TOKEN-DENY-01:
// when the ?verbose query parameter is present, the request must supply a
// matching token in a configured header; mismatch returns 401 JSON.
// Requests without ?verbose pass through unconditionally.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
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

// policyVerboseToken guards ?verbose mode with a shared secret header.
type policyVerboseToken struct {
	headerName string
	token      string
}

func (p *policyVerboseToken) Describe() string { return "verbose-token" }

func (p *policyVerboseToken) apply(mux *chi.Mux) {
	mux.Use(p.middleware())
}

func (p *policyVerboseToken) middleware() func(http.Handler) http.Handler {
	headerName := p.headerName
	token := p.token
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only enforce when ?verbose query parameter is present (any value,
			// including the bare "?verbose" form which url.Values.Has detects).
			if !r.URL.Query().Has("verbose") {
				next.ServeHTTP(w, r)
				return
			}
			// ?verbose present — require matching header.
			got := r.Header.Get(headerName)
			if got != token {
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
func PolicyVerboseToken(headerName, token string) *policyVerboseToken {
	if headerName == "" {
		panic("bootstrap: PolicyVerboseToken headerName must not be empty")
	}
	if token == "" {
		panic("bootstrap: PolicyVerboseToken token must not be empty")
	}
	return &policyVerboseToken{headerName: headerName, token: token}
}
