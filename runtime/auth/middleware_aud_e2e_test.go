package auth

// middleware_aud_e2e_test.go: real HTTP transport audience regression tests.
//
// TestAuthMiddleware_AudienceE2E_RealServer uses httptest.NewServer (real HTTP
// transport) to assert AuthMiddleware audience handling. The sibling
// middleware_aud_test.go covers the same matrix using httptest.ResponseRecorder.
//
// PR-A30 S24 AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestAuthMiddleware_AudienceE2E_RealServer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		tokenAud   []string // nil = no aud
		wantStatus int
		wantCode   errcode.Code // empty = success path
	}{
		{"right_audience_200", []string{"gocell"}, http.StatusOK, ""},
		{"wrong_audience_401", []string{"other-service"}, http.StatusUnauthorized, errcode.ErrAuthInvalidTokenIntent},
		{"missing_audience_401", nil, http.StatusUnauthorized, errcode.ErrAuthInvalidTokenIntent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			issuer, verifier := buildAudTestPair(t)
			opts := IssueOptions{}
			if tc.tokenAud != nil {
				opts.Audience = tc.tokenAud
			}
			token, err := issuer.Issue(TokenIntentAccess, "alice", opts)
			require.NoError(t, err)

			srv := httptest.NewServer(AuthMiddleware(verifier, WithAuthClock(clock.Real()))(audProtectedHandler))
			t.Cleanup(srv.Close)

			// Bind the request lifetime to the test's context so that go test's
			// own -timeout governs hangs — no hardcoded duration that drifts
			// against CI runner capacity.
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/protected", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			assert.Equal(t, tc.wantStatus, resp.StatusCode,
				"real-server response status mismatch for case %s", tc.name)

			// Reject paths must surface the audience-mismatch error inside the
			// middleware's logged chain. Direct verify confirms the error code
			// (string-free assertion — robust against message wording changes).
			if tc.wantCode != "" {
				_, verifyErr := verifier.VerifyIntent(t.Context(), token, TokenIntentAccess)
				require.Error(t, verifyErr)
				var ec *errcode.Error
				require.True(t, errors.As(verifyErr, &ec), "verify error must wrap *errcode.Error")
				assert.Equal(t, tc.wantCode, ec.Code, "case %s: error code", tc.name)
			}
		})
	}
}
