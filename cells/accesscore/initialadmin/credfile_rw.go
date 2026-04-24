//go:build unix || windows

package initialadmin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// writeCredentialFile atomically writes a credential file at path:
//  1. creates the credential directory with platform-restricted access;
//  2. creates a sibling .tmp file with O_EXCL|O_CREATE + mode 0o600;
//  3. writes the payload, renames the temp file into place, and reapplies
//     platform restrictions where the OS requires it.
//
// If path already exists, errCredFileExists is returned to prevent a second
// bootstrap run from silently overwriting an existing credential.
func writeCredentialFile(path string, payload credentialPayload, opts ...writeCredentialFileOption) error {
	cfg := &writeCredentialFileConfig{writer: formatPayload}
	for _, o := range opts {
		o(cfg)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", errCredFileExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("initialadmin: stat credential file %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := ensureSecureCredentialDir(dir); err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	_ = os.Remove(tmpPath)

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("initialadmin: create temp file %s: %w", tmpPath, err)
	}
	if err := secureCredentialTempFile(tmpPath); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("initialadmin: secure temp file %s: %w", tmpPath, err)
	}

	writeErr := cfg.writer(f, payload)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		if writeErr != nil {
			return fmt.Errorf("initialadmin: write credential payload: %w", writeErr)
		}
		return fmt.Errorf("initialadmin: close temp file: %w", closeErr)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("initialadmin: rename %s -> %s: %w", tmpPath, path, err)
	}
	if err := secureCredentialFinalFile(path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("initialadmin: secure credential file %s: %w", path, err)
	}
	return nil
}

// readCredentialExpiresAt reads the expires_at unix timestamp from the
// credential file at path and returns the corresponding time.Time (UTC).
// Returns an error when the file cannot be read, the expires_at line is
// missing, or the value cannot be parsed.
func readCredentialExpiresAt(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("initialadmin: read credential file: %w", err)
	}
	for _, line := range splitLines(string(data)) {
		const prefix = "expires_at="
		if len(line) > len(prefix) && line[:len(prefix)] == prefix {
			var ts int64
			if _, scanErr := fmt.Sscanf(line[len(prefix):], "%d", &ts); scanErr != nil {
				return time.Time{}, fmt.Errorf("initialadmin: parse expires_at: %w", scanErr)
			}
			return time.Unix(ts, 0).UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("initialadmin: expires_at not found in credential file")
}
