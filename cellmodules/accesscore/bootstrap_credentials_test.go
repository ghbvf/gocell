package accesscore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBootstrapCredentials_BothEmpty_ReturnsNilCreds(t *testing.T) {
	creds, err := loadBootstrapCredentials("", "")
	require.NoError(t, err)
	assert.Nil(t, creds.Username, "both empty should return nil Username")
	assert.Nil(t, creds.Password, "both empty should return nil Password")
}

func TestLoadBootstrapCredentials_UsernameOnlySet_ReturnsError(t *testing.T) {
	_, err := loadBootstrapCredentials("admin", "")
	require.Error(t, err, "username-only should return XOR error")
	assert.Contains(t, err.Error(), "both be set or both be empty")
}

func TestLoadBootstrapCredentials_PasswordOnlySet_ReturnsError(t *testing.T) {
	_, err := loadBootstrapCredentials("", "verysecretpass")
	require.Error(t, err, "password-only should return XOR error")
	assert.Contains(t, err.Error(), "both be set or both be empty")
}

func TestLoadBootstrapCredentials_UsernameWithControlChar_ReturnsError(t *testing.T) {
	// Null byte (U+0000) embedded in the middle is a control character and
	// is not stripped by strings.TrimSpace.
	_, err := loadBootstrapCredentials("ad\x00min", "validpassword")
	require.Error(t, err, "control character in username should return error")
	assert.Contains(t, err.Error(), "control characters")
}

func TestLoadBootstrapCredentials_PasswordTooShort_ReturnsError(t *testing.T) {
	// 7 bytes < 8 byte minimum.
	_, err := loadBootstrapCredentials("admin", "short7!")
	require.Error(t, err, "password shorter than 8 bytes should return error")
	assert.Contains(t, err.Error(), "at least 8 bytes")
}

func TestLoadBootstrapCredentials_BothValid_ReturnsCredentials(t *testing.T) {
	creds, err := loadBootstrapCredentials("admin", "validpassword123")
	require.NoError(t, err)
	assert.Equal(t, []byte("admin"), creds.Username)
	assert.Equal(t, []byte("validpassword123"), creds.Password)
}

func TestLoadBootstrapCredentials_PasswordExactlyMinLength_Accepted(t *testing.T) {
	// 8 bytes exactly should be accepted.
	_, err := loadBootstrapCredentials("admin", "12345678")
	require.NoError(t, err)
}

func TestLoadBootstrapCredentials_LeadingTrailingSpacesTrimmed(t *testing.T) {
	creds, err := loadBootstrapCredentials("  admin  ", "  validpassword  ")
	require.NoError(t, err)
	// TrimSpace means the stored bytes are the trimmed values.
	assert.Equal(t, []byte("admin"), creds.Username)
	assert.Equal(t, []byte("validpassword"), creds.Password)
}
