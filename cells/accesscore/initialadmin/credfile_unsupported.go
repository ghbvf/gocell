//go:build !unix && !windows

package initialadmin

import (
	"fmt"
	"time"
)

// writeCredentialFile is not supported on this platform.
// Returns errUnsupportedPlatform on all calls.
func writeCredentialFile(_ string, _ credentialPayload, _ ...writeCredentialFileOption) error {
	return fmt.Errorf("%w: writeCredentialFile", errUnsupportedPlatform)
}

// removeCredentialFile is not supported on this platform.
// Returns errUnsupportedPlatform on all calls.
func removeCredentialFile(_ string) error {
	return fmt.Errorf("%w: removeCredentialFile", errUnsupportedPlatform)
}

// readCredentialExpiresAt is not supported on this platform.
// Returns errUnsupportedPlatform on all calls.
func readCredentialExpiresAt(_ string) (time.Time, error) {
	return time.Time{}, fmt.Errorf("%w: readCredentialExpiresAt", errUnsupportedPlatform)
}
