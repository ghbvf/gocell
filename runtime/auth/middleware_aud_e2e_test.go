package auth

// middleware_aud_e2e_test.go: real HTTP transport audience regression tests.
//
// TestAuthMiddleware_AudienceE2E_RealServer uses httptest.NewServer (real HTTP
// transport) to assert AuthMiddleware audience handling. The sibling
// middleware_aud_test.go covers the same matrix using httptest.ResponseRecorder.
//
// PR-A30 S24 AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthMiddleware_AudienceE2E_RealServer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		tokenAud   []string // nil = no aud
		wantStatus int
	}{
		{"right_audience_200", []string{"gocell"}, http.StatusOK},
		{"wrong_audience_401", []string{"other-service"}, http.StatusUnauthorized},
		{"missing_audience_401", nil, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			issuer, verifier := buildAudTestPair(t)
			opts := IssueOptions{}
			if tc.tokenAud != nil {
				opts.Audience = tc.tokenAud
			}
			token, err := issuer.Issue(TokenIntentAccess, "alice", opts)
			require.NoError(t, err)

			srv := httptest.NewServer(AuthMiddleware(verifier)(audProtectedHandler))
			t.Cleanup(srv.Close)

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/protected", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			assert.Equal(t, tc.wantStatus, resp.StatusCode,
				"real-server response status mismatch for case %s", tc.name)
		})
	}
}
