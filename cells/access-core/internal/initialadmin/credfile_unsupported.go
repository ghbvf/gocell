//go:build !unix

package initialadmin

import (
	"fmt"
	"time"
)

// WriteCredentialFile is not supported on non-unix platforms.
// Returns ErrUnsupportedPlatform on all calls.
func WriteCredentialFile(_ string, _ CredentialPayload, _ ...WriteCredentialFileOption) error {
	return fmt.Errorf("%w: WriteCredentialFile", ErrUnsupportedPlatform)
}

// RemoveCredentialFile is not supported on non-unix platforms.
// Returns ErrUnsupportedPlatform on all calls.
func RemoveCredentialFile(_ string) error {
	return fmt.Errorf("%w: RemoveCredentialFile", ErrUnsupportedPlatform)
}

// ReadCredentialExpiresAt is not supported on non-unix platforms.
// Returns ErrUnsupportedPlatform on all calls.
func ReadCredentialExpiresAt(_ string) (time.Time, error) {
	return time.Time{}, fmt.Errorf("%w: ReadCredentialExpiresAt", ErrUnsupportedPlatform)
}

// ResolveCredentialPath is not supported on non-unix platforms.
// Returns ErrUnsupportedPlatform on all calls.
func ResolveCredentialPath(_ string) (string, error) {
	return "", fmt.Errorf("%w: ResolveCredentialPath", ErrUnsupportedPlatform)
}
