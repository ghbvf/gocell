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

// --- TestSecured ---

func TestSecured(t *testing.T) {
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

			h := Secured(inner, tc.policy)
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

// TestSecured_NilPolicy_Panics verifies that passing a nil policy to Secured
// panics immediately at wrap time, making misuse detectable at startup/test
// rather than silently skipping authorization at request time.
func TestSecured_NilPolicy_Panics(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	require.Panics(t, func() { Secured(inner, nil) })
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
			name:    "principal present with empty subject — authenticated (subject validation is caller's responsibility)",
			ctx:     WithPrincipal(context.Background(), &Principal{Subject: "", Kind: PrincipalUser}),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := buildRequest(tc.ctx)
			err := Authenticated()(r)

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
