package authtest_test

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/authtest"
)

func TestRequireAuthenticated(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() *auth.Principal
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:     "no principal",
			setup:    func() *auth.Principal { return nil },
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name: "anonymous principal",
			setup: func() *auth.Principal {
				return &auth.Principal{Kind: auth.PrincipalAnonymous}
			},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name: "user with empty subject",
			setup: func() *auth.Principal {
				return &auth.Principal{Kind: auth.PrincipalUser, Subject: ""}
			},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name: "user with subject",
			setup: func() *auth.Principal {
				return &auth.Principal{Kind: auth.PrincipalUser, Subject: "u-1"}
			},
			wantErr: false,
		},
		{
			name: "service principal",
			setup: func() *auth.Principal {
				return &auth.Principal{Kind: auth.PrincipalService, Subject: "svc-a"}
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if p := tc.setup(); p != nil {
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}

			policy := authtest.RequireAuthenticated()
			err := policy(r)

			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr), "expected errcode.Error, got %T: %v", err, err)
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}
