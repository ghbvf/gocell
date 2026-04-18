package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
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

// --- TestGuard ---

func TestGuard(t *testing.T) {
	tests := []struct {
		name       string
		policy     Policy
		wantOK     bool
		wantStatus int
		wantCode   string
	}{
		{
			name:       "policy nil — Guard returns true, response untouched",
			policy:     func(_ context.Context) error { return nil },
			wantOK:     true,
			wantStatus: 200,
		},
		{
			name: "policy ErrAuthUnauthorized — Guard returns false, 401",
			policy: func(_ context.Context) error {
				return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
			},
			wantOK:     false,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "ERR_AUTH_UNAUTHORIZED",
		},
		{
			name: "policy ErrAuthForbidden — Guard returns false, 403",
			policy: func(_ context.Context) error {
				return errcode.New(errcode.ErrAuthForbidden, "access denied")
			},
			wantOK:     false,
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
		{
			name:       "AnyRole empty roles — Guard returns false, 403 ERR_AUTH_FORBIDDEN",
			policy:     AnyRole(),
			wantOK:     false,
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := buildRequest(TestContext("user-1", []string{"admin"}))

			got := Guard(w, r, tc.policy)

			assert.Equal(t, tc.wantOK, got)
			assert.Equal(t, tc.wantStatus, w.Code)

			if !tc.wantOK {
				errObj := assertErrorBody(t, w)
				assert.Equal(t, tc.wantCode, errObj["code"])
				assert.NotNil(t, errObj["details"])
			} else {
				// Success path: response body must be empty (no write occurred).
				assert.Empty(t, w.Body.String())
			}
		})
	}
}

// TestGuard_NilPolicy_Panics verifies that passing a nil policy to Guard panics
// immediately, making misuse detectable at test time rather than silently
// skipping authorization.
func TestGuard_NilPolicy_Panics(t *testing.T) {
	w := httptest.NewRecorder()
	r := buildRequest(context.Background())
	require.Panics(t, func() { Guard(w, r, nil) })
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
			name:     "empty string subject — ErrAuthUnauthorized",
			ctx:      ctxkeys.WithSubject(context.Background(), ""),
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Authenticated()(tc.ctx)

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
			err := AnyRole(tc.policyRoles...)(tc.ctx)

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
		targetID    string
		bypassRoles []string
		wantErr     bool
		wantCode    errcode.Code
	}{
		{
			name:     "subject == targetID — nil",
			ctx:      TestContext("user-1", nil),
			targetID: "user-1",
			wantErr:  false,
		},
		{
			name:        "subject != targetID, bypass role matches — nil",
			ctx:         TestContext("user-2", []string{"admin"}),
			targetID:    "user-1",
			bypassRoles: []string{"admin"},
			wantErr:     false,
		},
		{
			name:        "subject != targetID, no bypass — ErrAuthForbidden",
			ctx:         TestContext("user-2", []string{"viewer"}),
			targetID:    "user-1",
			bypassRoles: []string{"admin"},
			wantErr:     true,
			wantCode:    errcode.ErrAuthForbidden,
		},
		{
			name:        "no subject — ErrAuthUnauthorized",
			ctx:         context.Background(),
			targetID:    "user-1",
			bypassRoles: []string{"admin"},
			wantErr:     true,
			wantCode:    errcode.ErrAuthUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := SelfOr(tc.targetID, tc.bypassRoles...)(tc.ctx)

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
