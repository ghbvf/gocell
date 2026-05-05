package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestNewUser(t *testing.T) {
	tests := []struct {
		name         string
		username     string
		email        string
		passwordHash string
		wantErr      bool
		errMsg       string
	}{
		{
			name:         "valid user",
			username:     "alice",
			email:        "alice@example.com",
			passwordHash: "$2a$10$hash",
			wantErr:      false,
		},
		{
			name:         "empty username",
			username:     "",
			email:        "alice@example.com",
			passwordHash: "$2a$10$hash",
			wantErr:      true,
			errMsg:       "username",
		},
		{
			name:         "empty email",
			username:     "alice",
			email:        "",
			passwordHash: "$2a$10$hash",
			wantErr:      true,
			errMsg:       "email",
		},
		{
			name:         "empty passwordHash",
			username:     "alice",
			email:        "alice@example.com",
			passwordHash: "",
			wantErr:      true,
			errMsg:       "passwordHash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := NewUser(tt.username, tt.email, tt.passwordHash, time.Now())
			if tt.wantErr {
				require.Error(t, err)
				// Lock errcode classification independently of the message
				// format so future helper rewrites (e.g. localization) do
				// not silently weaken the contract.
				var coded *errcode.Error
				require.ErrorAs(t, err, &coded, "expected an errcode.Error")
				assert.Equal(t, errcode.ErrAuthInvalidInput, coded.Code,
					"NewUser must surface ErrAuthInvalidInput on blank fields")
				assert.Equal(t, "validation: required field missing", coded.Message,
					"message must be a const literal")
				var gotField string
				for _, attr := range coded.Details {
					if attr.Key == "field" {
						gotField = attr.Value.String()
						break
					}
				}
				assert.Equal(t, tt.errMsg, gotField, "details must carry the field name")
				assert.Nil(t, user)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.username, user.Username)
			assert.Equal(t, tt.email, user.Email)
			assert.Equal(t, tt.passwordHash, user.PasswordHash)
			assert.Equal(t, StatusActive, user.Status)
			assert.Equal(t, UserSourceIdentity, user.CreationSource)
			assert.False(t, user.CreatedAt.IsZero())
			assert.False(t, user.UpdatedAt.IsZero())
		})
	}
}

func TestUser_LockUnlock(t *testing.T) {
	tests := []struct {
		name       string
		action     func(u *User)
		wantLocked bool
	}{
		{
			name:       "new user is not locked",
			action:     func(u *User) {},
			wantLocked: false,
		},
		{
			name:       "lock sets locked",
			action:     func(u *User) { u.LockAccount(time.Now()) },
			wantLocked: true,
		},
		{
			name: "unlock after lock",
			action: func(u *User) {
				u.LockAccount(time.Now())
				u.UnlockAccount(time.Now())
			},
			wantLocked: false,
		},
		{
			name: "double lock remains locked",
			action: func(u *User) {
				u.LockAccount(time.Now())
				u.LockAccount(time.Now())
			},
			wantLocked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := NewUser("bob", "bob@example.com", "$2a$10$hash", time.Now())
			require.NoError(t, err)

			tt.action(user)

			assert.Equal(t, tt.wantLocked, user.IsLocked())
		})
	}
}

func TestUser_Lock_UpdatesTimestamp(t *testing.T) {
	user, err := NewUser("charlie", "charlie@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)

	before := user.UpdatedAt
	user.LockAccount(time.Now())
	assert.True(t, !user.UpdatedAt.Before(before), "UpdatedAt should advance after Lock")
}

func TestUser_DefaultPasswordResetRequiredFalse(t *testing.T) {
	user, err := NewUser("dave", "dave@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	assert.False(t, user.PasswordResetRequired, "NewUser must default PasswordResetRequired to false")
}

func TestUser_MarkPasswordResetRequiredSetsFlag(t *testing.T) {
	user, err := NewUser("eve", "eve@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	require.False(t, user.PasswordResetRequired)

	before := user.UpdatedAt
	user.MarkPasswordResetRequired(time.Now())

	assert.True(t, user.PasswordResetRequired, "MarkPasswordResetRequired must set flag to true")
	assert.True(t, !user.UpdatedAt.Before(before), "MarkPasswordResetRequired must advance UpdatedAt")
}

func TestUser_ClearPasswordResetRequiredUnsets(t *testing.T) {
	user, err := NewUser("frank", "frank@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	user.MarkPasswordResetRequired(time.Now())
	require.True(t, user.PasswordResetRequired)

	before := user.UpdatedAt
	user.ClearPasswordResetRequired(time.Now())

	assert.False(t, user.PasswordResetRequired, "ClearPasswordResetRequired must set flag to false")
	assert.True(t, !user.UpdatedAt.Before(before), "ClearPasswordResetRequired must advance UpdatedAt")
}

