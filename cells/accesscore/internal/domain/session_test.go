package domain

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSession(t *testing.T) {
	future := time.Now().Add(testtime.D1h)

	tests := []struct {
		name        string
		userID      string
		accessToken string
		expiresAt   time.Time
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "valid session",
			userID:      "u-1",
			accessToken: "at-abc",
			expiresAt:   future,
			wantErr:     false,
		},
		{
			name:        "empty userID",
			userID:      "",
			accessToken: "at-abc",
			expiresAt:   future,
			wantErr:     true,
			errMsg:      "userID is required",
		},
		{
			name:        "empty accessToken",
			userID:      "u-1",
			accessToken: "",
			expiresAt:   future,
			wantErr:     true,
			errMsg:      "accessToken is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := NewSession(tt.userID, tt.accessToken, tt.expiresAt)
			if tt.wantErr {
				require.Error(t, err)
				// Lock the errcode classification — survives helper message
				// format changes (e.g. localization) without losing coverage.
				var coded *errcode.Error
				require.ErrorAs(t, err, &coded, "expected an errcode.Error")
				assert.Equal(t, errcode.ErrAuthSessionInvalidInput, coded.Code,
					"NewSession must surface ErrAuthSessionInvalidInput on blank fields")
				assert.Contains(t, err.Error(), tt.errMsg,
					"current message format is %q-style; helper rewrites must keep field name visible",
					"<field> is required")
				assert.Nil(t, session)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.userID, session.UserID)
			assert.Equal(t, tt.accessToken, session.AccessToken)
			assert.Equal(t, tt.expiresAt, session.ExpiresAt)
			assert.Nil(t, session.RevokedAt)
			assert.False(t, session.CreatedAt.IsZero())
		})
	}
}

func TestSession_Revoke(t *testing.T) {
	tests := []struct {
		name        string
		action      func(s *Session)
		wantRevoked bool
	}{
		{
			name:        "new session is not revoked",
			action:      func(s *Session) {},
			wantRevoked: false,
		},
		{
			name:        "revoke marks session revoked",
			action:      func(s *Session) { s.Revoke() },
			wantRevoked: true,
		},
		{
			name: "double revoke stays revoked",
			action: func(s *Session) {
				s.Revoke()
				s.Revoke()
			},
			wantRevoked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := NewSession("u-1", "at", time.Now().Add(time.Hour))
			require.NoError(t, err)

			tt.action(session)

			assert.Equal(t, tt.wantRevoked, session.IsRevoked())
		})
	}
}

func TestSession_IsExpired(t *testing.T) {
	tests := []struct {
		name        string
		expiresAt   time.Time
		wantExpired bool
	}{
		{
			name:        "future expiry is not expired",
			expiresAt:   time.Now().Add(testtime.D1h),
			wantExpired: false,
		},
		{
			name:        "past expiry is expired",
			expiresAt:   time.Now().Add(testtime.DNeg1h),
			wantExpired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := NewSession("u-1", "at", tt.expiresAt)
			require.NoError(t, err)

			assert.Equal(t, tt.wantExpired, session.IsExpired())
		})
	}
}
