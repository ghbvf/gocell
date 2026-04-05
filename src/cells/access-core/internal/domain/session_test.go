package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSession(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)

	tests := []struct {
		name         string
		userID       string
		accessToken  string
		refreshToken string
		expiresAt    time.Time
		wantErr      bool
		errMsg       string
	}{
		{
			name:         "valid session",
			userID:       "u-1",
			accessToken:  "at-abc",
			refreshToken: "rt-xyz",
			expiresAt:    future,
			wantErr:      false,
		},
		{
			name:         "empty userID",
			userID:       "",
			accessToken:  "at-abc",
			refreshToken: "rt-xyz",
			expiresAt:    future,
			wantErr:      true,
			errMsg:       "userID is required",
		},
		{
			name:         "empty accessToken",
			userID:       "u-1",
			accessToken:  "",
			refreshToken: "rt-xyz",
			expiresAt:    future,
			wantErr:      true,
			errMsg:       "accessToken is required",
		},
		{
			name:         "empty refreshToken",
			userID:       "u-1",
			accessToken:  "at-abc",
			refreshToken: "",
			expiresAt:    future,
			wantErr:      true,
			errMsg:       "refreshToken is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := NewSession(tt.userID, tt.accessToken, tt.refreshToken, tt.expiresAt)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, session)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.userID, session.UserID)
			assert.Equal(t, tt.accessToken, session.AccessToken)
			assert.Equal(t, tt.refreshToken, session.RefreshToken)
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
			session, err := NewSession("u-1", "at", "rt", time.Now().Add(time.Hour))
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
			expiresAt:   time.Now().Add(1 * time.Hour),
			wantExpired: false,
		},
		{
			name:        "past expiry is expired",
			expiresAt:   time.Now().Add(-1 * time.Hour),
			wantExpired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := NewSession("u-1", "at", "rt", tt.expiresAt)
			require.NoError(t, err)

			assert.Equal(t, tt.wantExpired, session.IsExpired())
		})
	}
}
