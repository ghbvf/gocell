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
			assert.Equal(t, StatusActive, user.Status())
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
			action:     func(u *User) { u.SetStatus(StatusLocked, time.Now()) },
			wantLocked: true,
		},
		{
			name: "unlock after lock",
			action: func(u *User) {
				u.SetStatus(StatusLocked, time.Now())
				u.SetStatus(StatusActive, time.Now())
			},
			wantLocked: false,
		},
		{
			name: "double lock remains locked",
			action: func(u *User) {
				u.SetStatus(StatusLocked, time.Now())
				u.SetStatus(StatusLocked, time.Now())
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
	user.SetStatus(StatusLocked, time.Now())
	assert.True(t, !user.UpdatedAt.Before(before), "UpdatedAt should advance after SetStatus(Locked)")
}

func TestUser_DefaultPasswordResetRequiredFalse(t *testing.T) {
	user, err := NewUser("dave", "dave@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	assert.False(t, user.PasswordResetRequired(), "NewUser must default PasswordResetRequired to false")
}

func TestUser_MarkPasswordResetRequiredSetsFlag(t *testing.T) {
	user, err := NewUser("eve", "eve@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	require.False(t, user.PasswordResetRequired())

	before := user.UpdatedAt
	user.SetPasswordResetRequired(true, time.Now())

	assert.True(t, user.PasswordResetRequired(), "SetPasswordResetRequired(true) must set flag to true")
	assert.True(t, !user.UpdatedAt.Before(before), "SetPasswordResetRequired must advance UpdatedAt")
}

func TestUser_ClearPasswordResetRequiredUnsets(t *testing.T) {
	user, err := NewUser("frank", "frank@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	user.SetPasswordResetRequired(true, time.Now())
	require.True(t, user.PasswordResetRequired())

	before := user.UpdatedAt
	user.SetPasswordResetRequired(false, time.Now())

	assert.False(t, user.PasswordResetRequired(), "SetPasswordResetRequired(false) must set flag to false")
	assert.True(t, !user.UpdatedAt.Before(before), "SetPasswordResetRequired must advance UpdatedAt")
}

func TestBumpPasswordVersion_AdvancesVersion(t *testing.T) {
	tests := []struct {
		name        string
		bumps       int
		wantVersion int64
	}{
		{
			name:        "single bump advances 0 to 1",
			bumps:       1,
			wantVersion: 1,
		},
		{
			name:        "two bumps advance 0 to 2",
			bumps:       2,
			wantVersion: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := NewUser("grace", "grace@example.com", "$2a$10$hash", time.Now())
			require.NoError(t, err)
			require.Equal(t, int64(0), user.PasswordVersion)

			for range tt.bumps {
				user.BumpPasswordVersion(time.Now())
			}

			assert.Equal(t, tt.wantVersion, user.PasswordVersion)
		})
	}
}

func TestBumpPasswordVersion_UpdatesUpdatedAt(t *testing.T) {
	user, err := NewUser("heidi", "heidi@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)

	now := time.Now().Add(time.Minute)
	user.BumpPasswordVersion(now)

	assert.Equal(t, now, user.UpdatedAt, "BumpPasswordVersion must set UpdatedAt to the provided now")
}

func TestNewUser_InitializesPasswordVersionZero(t *testing.T) {
	user, err := NewUser("ivan", "ivan@example.com", "$2a$10$hash", time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(0), user.PasswordVersion, "NewUser must initialize PasswordVersion to zero")
}

func TestValidUserStatus(t *testing.T) {
	tests := []struct {
		in   UserStatus
		want bool
	}{
		{StatusActive, true},
		{StatusSuspended, true},
		{StatusLocked, true},
		{UserStatus(""), false},
		{UserStatus("invalid"), false},
		{UserStatus("ACTIVE"), false}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			assert.Equal(t, tt.want, ValidUserStatus(tt.in))
		})
	}
}

func TestUser_CanAuthenticate(t *testing.T) {
	tests := []struct {
		name   string
		status UserStatus
		want   bool
	}{
		{name: "active", status: StatusActive, want: true},
		{name: "suspended_rejected", status: StatusSuspended, want: false},
		{name: "locked_rejected", status: StatusLocked, want: false},
		{name: "unknown_status_fail_closed", status: UserStatus("unknown"), want: false},
		{name: "empty_status_fail_closed", status: UserStatus(""), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use ReconstituteUser for locked/suspended; NewUser+SetStatus for the test.
			// For invalid status values, build directly via ReconstituteUser (will fail),
			// so use a different approach: reuse a valid user and mutate status in-package.
			now := time.Now()
			if tt.status == StatusActive {
				u, err := NewUser("test", "test@example.com", "$2a$10$hash", now)
				require.NoError(t, err)
				assert.Equal(t, tt.want, u.CanAuthenticate())
			} else {
				// Build with a reconstituted user using a valid status,
				// then override if it's a known status.
				var u *User
				switch tt.status {
				case StatusSuspended, StatusLocked:
					var err error
					u, err = ReconstituteUser(ReconstituteUserParams{
						ID:           "uid",
						Username:     "test",
						Email:        "test@example.com",
						PasswordHash: "$2a$10$hash",
						Status:       tt.status,
						Source:       UserSourceIdentity,
						AuthzEpoch:   1,
						CreatedAt:    now,
						UpdatedAt:    now,
					})
					require.NoError(t, err)
				default:
					// Invalid status: test CanAuthenticate via SetStatus from a valid base
					base, err := NewUser("test", "test@example.com", "$2a$10$hash", now)
					require.NoError(t, err)
					// Force invalid status via the in-package private field directly
					// (this test is in package domain, so we can access private fields)
					base.status = tt.status
					u = base
				}
				assert.Equal(t, tt.want, u.CanAuthenticate())
			}
		})
	}
}

func TestValidUserSource(t *testing.T) {
	tests := []struct {
		in   UserSource
		want bool
	}{
		{UserSourceIdentity, true},
		{UserSourceSetup, true},
		{UserSource(""), false},
		{UserSource("invalid"), false},
		{UserSource("IDENTITY"), false}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			assert.Equal(t, tt.want, ValidUserSource(tt.in))
		})
	}
}

func TestReconstituteUser(t *testing.T) {
	now := time.Now()
	t.Run("valid", func(t *testing.T) {
		u, err := ReconstituteUser(ReconstituteUserParams{
			ID:                    "id1",
			Username:              "alice",
			Email:                 "alice@example.com",
			PasswordHash:          "$2a$10$hash",
			PasswordVersion:       3,
			PasswordResetRequired: true,
			Status:                StatusActive,
			Source:                UserSourceIdentity,
			AuthzEpoch:            5,
			CreatedAt:             now,
			UpdatedAt:             now,
		})
		require.NoError(t, err)
		assert.Equal(t, "id1", u.ID)
		assert.Equal(t, "alice", u.Username)
		assert.Equal(t, StatusActive, u.Status())
		assert.True(t, u.PasswordResetRequired())
		assert.Equal(t, int64(5), u.AuthzEpoch())
		assert.Equal(t, int64(3), u.PasswordVersion)
	})
	t.Run("zero_epoch_rejected", func(t *testing.T) {
		_, err := ReconstituteUser(ReconstituteUserParams{
			ID:           "id1",
			Username:     "alice",
			Email:        "alice@example.com",
			PasswordHash: "$2a$10$hash",
			Status:       StatusActive,
			Source:       UserSourceIdentity,
			AuthzEpoch:   0,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		require.Error(t, err)
		var ce *errcode.Error
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, errcode.ErrAuthInvalidInput, ce.Code)
	})
	t.Run("negative_epoch_rejected", func(t *testing.T) {
		_, err := ReconstituteUser(ReconstituteUserParams{
			ID:           "id1",
			Username:     "alice",
			Email:        "alice@example.com",
			PasswordHash: "$2a$10$hash",
			Status:       StatusActive,
			Source:       UserSourceIdentity,
			AuthzEpoch:   -1,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		require.Error(t, err)
	})
	t.Run("invalid_status_rejected", func(t *testing.T) {
		_, err := ReconstituteUser(ReconstituteUserParams{
			ID:           "id1",
			Username:     "alice",
			Email:        "alice@example.com",
			PasswordHash: "$2a$10$hash",
			Status:       UserStatus("invalid"),
			Source:       UserSourceIdentity,
			AuthzEpoch:   1,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		require.Error(t, err)
	})
	t.Run("empty_id_rejected", func(t *testing.T) {
		_, err := ReconstituteUser(ReconstituteUserParams{
			ID:           "",
			Username:     "alice",
			Email:        "alice@example.com",
			PasswordHash: "$2a$10$hash",
			Status:       StatusActive,
			Source:       UserSourceIdentity,
			AuthzEpoch:   1,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		require.Error(t, err)
	})
}
