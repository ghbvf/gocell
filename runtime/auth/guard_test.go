package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireAuthenticatedPolicy mirrors authtest.RequireAuthenticated() inside
// package auth to avoid an import cycle (auth → authtest → auth). It is
// strictly for white-box tests in this package; never copy it into cells/* or
// examples/* — those should use auth.AnyRole(...) with a named role and
// auth.TestContext(...) for principal injection.
func requireAuthenticatedPolicy() Policy {
	return func(r *http.Request) error {
		p, ok := FromContext(r.Context())
		if !ok {
			return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
		}
		if p.Kind == PrincipalAnonymous {
			return errcode.New(errcode.ErrAuthUnauthorized, "anonymous principal not permitted")
		}
		if p.Kind == PrincipalUser && p.Subject == "" {
			return errcode.New(errcode.ErrAuthUnauthorized, "principal subject missing")
		}
		return nil
	}
}

// buildRequest returns a GET / request carrying the given context.
func buildRequest(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
}

// assertErrorBody decodes the response JSON and returns the "error" object.
func assertErrorBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "response must contain an 'error' object")
	return errObj
}

// --- TestRequirePolicy (replacing the legacy auth.Secured helper removed in F3) ---

// TestRequirePolicy_LegacySecuredBehavior verifies that RequirePolicy preserves
// the short-circuit and error-mapping behavior that callers previously relied on
// from the legacy auth.Secured helper (removed in F3).
func TestRequirePolicy_LegacySecuredBehavior(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		policy     Policy
		wantStatus int
		wantCode   string
	}{
		{
			name:       "policy permits — inner handler called, 200",
			policy:     func(_ *http.Request) error { return nil },
			wantStatus: http.StatusOK,
		},
		{
			name: "policy ErrAuthUnauthorized — short-circuits, 401",
			policy: func(_ *http.Request) error {
				return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
			},
			wantStatus: http.StatusUnauthorized,
			wantCode:   "ERR_AUTH_UNAUTHORIZED",
		},
		{
			name: "policy ErrAuthForbidden — short-circuits, 403",
			policy: func(_ *http.Request) error {
				return errcode.New(errcode.ErrAuthForbidden, "access denied")
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
		{
			name:       "AnyRole empty roles — short-circuits, 403",
			policy:     AnyRole(),
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := buildRequest(TestContext("user-1", []string{"admin"}))

			middleware, err := RequirePolicy(tc.policy)
			require.NoError(t, err)
			h := middleware(inner)
			h.ServeHTTP(w, r)

			assert.Equal(t, tc.wantStatus, w.Code)

			if tc.wantCode != "" {
				errObj := assertErrorBody(t, w)
				assert.Equal(t, tc.wantCode, errObj["code"])
				assert.NotNil(t, errObj["details"])
			}
		})
	}
}

// TestRequirePolicy_NilPolicy_ReturnsError verifies that passing a nil policy
// to RequirePolicy is rejected at wrap time rather than silently skipping
// authorization at request time.
func TestRequirePolicy_NilPolicy_ReturnsError(t *testing.T) {
	middleware, err := RequirePolicy(nil)
	require.Error(t, err)
	assert.Nil(t, middleware)
	assert.Contains(t, err.Error(), "policy must not be nil")
}

// --- TestAuthenticated ---

func TestAuthenticated(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:    "subject present — nil",
			ctx:     TestContext("user-1", nil),
			wantErr: false,
		},
		{
			name:     "no subject in ctx — ErrAuthUnauthorized",
			ctx:      context.Background(),
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "principal present with empty subject — ErrAuthUnauthorized (subject invariant enforced at authz entry)",
			ctx:      WithPrincipal(context.Background(), &Principal{Subject: "", Kind: PrincipalUser}),
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := buildRequest(tc.ctx)
			err := requireAuthenticatedPolicy()(r)

			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

// --- TestAnyRole ---

func TestAnyRole(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		policyRoles []string
		wantErr     bool
		wantCode    errcode.Code
	}{
		{
			name:        "subject present, role matches — nil",
			ctx:         TestContext("user-1", []string{"admin"}),
			policyRoles: []string{"admin"},
			wantErr:     false,
		},
		{
			name:        "subject present, no matching role — ErrAuthForbidden",
			ctx:         TestContext("user-1", []string{"viewer"}),
			policyRoles: []string{"admin"},
			wantErr:     true,
			wantCode:    errcode.ErrAuthForbidden,
		},
		{
			name:        "no subject in ctx — ErrAuthUnauthorized",
			ctx:         context.Background(),
			policyRoles: []string{"admin"},
			wantErr:     true,
			wantCode:    errcode.ErrAuthUnauthorized,
		},
		{
			name:        "empty roles — ErrAuthForbidden (hasAnyRole returns false when roles empty)",
			ctx:         TestContext("user-1", []string{"admin"}),
			policyRoles: []string{},
			wantErr:     true,
			wantCode:    errcode.ErrAuthForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := buildRequest(tc.ctx)
			err := AnyRole(tc.policyRoles...)(r)

			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

// --- TestSelfOr ---

func TestSelfOr(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		pathParam   string
		pathValue   string
		bypassRoles []string
		wantErr     bool
		wantCode    errcode.Code
	}{
		{
			name:      "subject == path value — nil",
			ctx:       TestContext("user-1", nil),
			pathParam: "id",
			pathValue: "user-1",
			wantErr:   false,
		},
		{
			name:        "subject != path value, bypass role matches — nil",
			ctx:         TestContext("user-2", []string{"admin"}),
			pathParam:   "id",
			pathValue:   "user-1",
			bypassRoles: []string{"admin"},
			wantErr:     false,
		},
		{
			name:        "subject != path value, no bypass — ErrAuthForbidden",
			ctx:         TestContext("user-2", []string{"viewer"}),
			pathParam:   "id",
			pathValue:   "user-1",
			bypassRoles: []string{"admin"},
			wantErr:     true,
			wantCode:    errcode.ErrAuthForbidden,
		},
		{
			name:        "no subject — ErrAuthUnauthorized",
			ctx:         context.Background(),
			pathParam:   "id",
			pathValue:   "user-1",
			bypassRoles: []string{"admin"},
			wantErr:     true,
			wantCode:    errcode.ErrAuthUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := buildRequest(tc.ctx)
			r.SetPathValue(tc.pathParam, tc.pathValue)
			err := SelfOr(tc.pathParam, tc.bypassRoles...)(r)

			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

// TestSelfOr_UUIDNormalization verifies Finding 11: SelfOr normalizes UUID
// path-value format variants before subject comparison so that uppercase or
// compact UUID representations are treated identically to canonical lowercase.
func TestSelfOr_UUIDNormalization(t *testing.T) {
	const canonicalUUID = "0e8d6e9a-3a6f-4b1f-9c1e-2a3b4c5d6e7f"

	tests := []struct {
		name      string
		subject   string // stored in Principal (canonical lowercase)
		pathValue string // raw path value (may be in any UUID format)
		wantErr   bool
	}{
		{
			name:      "self-access canonical lowercase UUID — Allow",
			subject:   canonicalUUID,
			pathValue: canonicalUUID,
			wantErr:   false,
		},
		{
			name:      "self-access uppercase UUID — Allow after normalization",
			subject:   canonicalUUID,
			pathValue: "0E8D6E9A-3A6F-4B1F-9C1E-2A3B4C5D6E7F",
			wantErr:   false,
		},
		{
			name:      "self-access compact 32-char hex UUID — Allow after normalization",
			subject:   canonicalUUID,
			pathValue: "0e8d6e9a3a6f4b1f9c1e2a3b4c5d6e7f",
			wantErr:   false,
		},
		{
			name:      "different UUID — Deny",
			subject:   canonicalUUID,
			pathValue: "11111111-1111-1111-1111-111111111111",
			wantErr:   true,
		},
		{
			// google/uuid.Parse silently accepts brace-wrapped GUIDs and would
			// have previously normalized to canonical, allowing self-access via a
			// non-canonical wire form. ParseCanonicalUUID rejects length-38 inputs
			// so the raw "{...}" string is compared verbatim against the canonical
			// subject and never matches.
			name:      "self-access brace-wrapped UUID — Deny (strict canonical)",
			subject:   canonicalUUID,
			pathValue: "{" + canonicalUUID + "}",
			wantErr:   true,
		},
		{
			// google/uuid.Parse accepts urn:uuid: prefixed form (length 45). Same
			// rationale: not a canonical wire form → no normalization → mismatch.
			name:      "self-access urn:uuid prefixed — Deny (strict canonical)",
			subject:   canonicalUUID,
			pathValue: "urn:uuid:" + canonicalUUID,
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := TestContext(tc.subject, nil)
			r := buildRequest(ctx)
			r.SetPathValue("id", tc.pathValue)
			err := SelfOr("id")(r)
			if tc.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr))
				assert.Equal(t, errcode.ErrAuthForbidden, ecErr.Code)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
