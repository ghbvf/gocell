package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireSelfOrRole(t *testing.T) {
	tests := []struct {
		name       string
		ctx        context.Context
		targetID   string
		roles      []string
		wantErr    bool
		wantCode   errcode.Code
	}{
		{
			name:     "self-access allowed",
			ctx:      withSubjectAndClaims("user-1", nil),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  false,
		},
		{
			name:     "admin bypass allowed",
			ctx:      withSubjectAndClaims("user-2", []string{"admin"}),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  false,
		},
		{
			name:     "different user no admin denied",
			ctx:      withSubjectAndClaims("user-2", []string{"viewer"}),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "missing subject denied",
			ctx:      context.Background(),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "empty targetID denied",
			ctx:      withSubjectAndClaims("user-1", nil),
			targetID: "",
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "multiple bypass roles second matches",
			ctx:      withSubjectAndClaims("user-2", []string{"operator"}),
			targetID: "user-1",
			roles:    []string{"admin", "operator"},
			wantErr:  false,
		},
		{
			name:     "no bypass roles specified only self allowed",
			ctx:      withSubjectAndClaims("user-2", []string{"admin"}),
			targetID: "user-1",
			roles:    nil,
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireSelfOrRole(tc.ctx, tc.targetID, tc.roles...)
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

func TestRequireAnyRole(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		roles    []string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:    "admin role allowed",
			ctx:     withSubjectAndClaims("user-1", []string{"admin"}),
			roles:   []string{"admin"},
			wantErr: false,
		},
		{
			name:    "second role matches",
			ctx:     withSubjectAndClaims("user-1", []string{"operator"}),
			roles:   []string{"admin", "operator"},
			wantErr: false,
		},
		{
			name:     "no matching role denied",
			ctx:      withSubjectAndClaims("user-1", []string{"viewer"}),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "no roles in claims denied",
			ctx:      withSubjectAndClaims("user-1", nil),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "missing subject denied",
			ctx:      context.Background(),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "empty string subject denied",
			ctx:      withSubjectAndClaims("", nil),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "empty required roles denied",
			ctx:      withSubjectAndClaims("user-1", []string{"admin"}),
			roles:    nil,
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireAnyRole(tc.ctx, tc.roles...)
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

func withSubjectAndClaims(subject string, roles []string) context.Context {
	ctx := ctxkeys.WithSubject(context.Background(), subject)
	ctx = WithClaims(ctx, Claims{Subject: subject, Roles: roles})
	return ctx
}
