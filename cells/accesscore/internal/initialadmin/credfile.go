//go:build unix

package initialadmin

import (
	"errors"
	"fmt"
	"os"
)

func ensureSecureCredentialDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("initialadmin: create directory %s: %w", dir, err)
	}
	return nil
}

func secureCredentialTempFile(string) error {
	return nil
}

func secureCredentialFinalFile(string) error {
	return nil
}

// RemoveCredentialFile safely removes the credential file at path:
//   - If the file does not exist, returns nil (idempotent).
//   - If the file's permission is not 0o600, removes the file unconditionally
//     (security intent: destroy the credential regardless of tampering) and
//     returns a wrapped ErrCredFileTampered so the caller can log the anomaly.
//   - Otherwise removes the file.
func RemoveCredentialFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("initialadmin: stat credential file: %w", err)
	}

	tampered := info.Mode().Perm() != 0o600

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("initialadmin: remove credential file: %w", err)
	}

	if tampered {
		return fmt.Errorf("%w: got mode %o, want 0600", ErrCredFileTampered, info.Mode().Perm())
	}

	return nil
}
