// PR-A30 S22 REFRESH-AUD-REAL-ROUTE-TEST-01.
//
// Refresh tokens are opaque (PR-A29) — audience is on the *response* access JWT,
// not on the input. This test asserts the chain: when issuer default audience
// and verifier expected audience disagree, the access JWT returned by
// POST /api/v1/access/sessions/refresh fails downstream verifier.VerifyIntent.
//
// The unit-level "default-aud-written-on-refresh" assertion is at
// cells/accesscore/slices/sessionrefresh/service_test.go::TestNewService_IssuerDefaultAudienceWrittenOnRefresh.
package accesscore

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthIntegration_RefreshAccessTokenAudienceDrift(t *testing.T) {
	cases := []struct {
		name         string
		issuerAuds   []string // empty = no WithIssuerAudiencesFromSlice option
		verifierAuds []string
		wantErrCode  errcode.Code // empty = expect verify success
	}{
		{"refresh_returns_aligned_aud_passes", []string{"gocell"}, []string{"gocell"}, ""},
		{"refresh_returns_drifted_aud_rejected", []string{"gocell-other"}, []string{"gocell"}, errcode.ErrAuthInvalidTokenIntent},
		// Login itself accepts the aud-less token because /sessions/login is Public:true
		// (no verifier on the request); the rejection surfaces only on the rotated access JWT.
		{"refresh_returns_no_default_aud_rejected", []string{}, []string{"gocell"}, errcode.ErrAuthInvalidTokenIntent},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Single-layer parallelism (only at subtest level): keeps fixture
			// build cost — bcrypt seeding, refresh-store init, login round-trip
			// — from compounding when the parent test also fans out under
			// `go test -p`.
			t.Parallel()
			fx := loginAndGetPair(t,
				withIssuerAuds(tc.issuerAuds...),
				withVerifierAuds(tc.verifierAuds...),
			)

			body := strings.NewReader(`{"refreshToken":"` + fx.RefreshToken + `"}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/access/sessions/refresh", body)
			req.Header.Set("Content-Type", "application/json")
			fx.Router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code,
				"case %s: refresh must return 200 (drift only matters on subsequent verify), got %d body=%s",
				tc.name, rec.Code, rec.Body.String())

			var envelope struct {
				Data struct {
					AccessToken  string `json:"accessToken"`
					RefreshToken string `json:"refreshToken"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			require.NotEmpty(t, envelope.Data.AccessToken,
				"case %s: refresh response must include accessToken", tc.name)

			_, err := fx.Verifier.VerifyIntent(t.Context(), envelope.Data.AccessToken, auth.TokenIntentAccess)
			if tc.wantErrCode == "" {
				require.NoError(t, err,
					"case %s: aligned audiences — verifier must accept refreshed access token", tc.name)
				return
			}
			require.Error(t, err,
				"case %s: drifted audiences — verifier must reject refreshed access token", tc.name)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "case %s: error must wrap *errcode.Error", tc.name)
			assert.Equal(t, tc.wantErrCode, ec.Code, "case %s: error code", tc.name)
		})
	}
}
