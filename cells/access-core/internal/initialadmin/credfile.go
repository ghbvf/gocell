//go:build unix

package initialadmin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// WriteCredentialFile atomically writes a credential file at path:
//  1. MkdirAll(dir, 0o700) — creates the directory with strict permissions.
//  2. Creates a sibling .tmp file with O_EXCL|O_CREATE + mode 0o600.
//  3. On success, os.Rename atomically replaces the target; on failure the .tmp
//     is removed.
//
// If path already exists, ErrCredFileExists is returned to prevent a second
// bootstrap run from silently overwriting an existing credential.
func WriteCredentialFile(path string, payload CredentialPayload, opts ...WriteCredentialFileOption) error {
	cfg := &writeCredentialFileConfig{writer: formatPayload}
	for _, o := range opts {
		o(cfg)
	}
	// Refuse to overwrite.
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrCredFileExists, path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("initialadmin: create directory %s: %w", dir, err)
	}

	tmpPath := path + ".tmp"

	// Remove any stale .tmp from a previous crash before creating.
	_ = os.Remove(tmpPath)

	// O_EXCL ensures we don't race with another process.
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("initialadmin: create temp file %s: %w", tmpPath, err)
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
		return fmt.Errorf("initialadmin: rename %s → %s: %w", tmpPath, path, err)
	}

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

// ReadCredentialExpiresAt reads the expires_at unix timestamp from the
// credential file at path and returns the corresponding time.Time (UTC).
// Returns an error when the file cannot be read, the expires_at line is
// missing, or the value cannot be parsed.
func ReadCredentialExpiresAt(path string) (time.Time, error) {
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

// splitLines splits s into non-empty lines (handles \n and \r\n).
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) && s[start:] != "" {
		lines = append(lines, s[start:])
	}
	return lines
}

// formatPayload serialises payload into w using the canonical file format:
//
//	# GoCell initial admin credential
//	# Generated at: <ISO8601>
//	# Expires at:   <ISO8601>
//	# This file is auto-deleted by the cleanup worker.
//	username=<username>
//	password=<password>
//	expires_at=<unix timestamp>
func formatPayload(w io.Writer, p CredentialPayload) error {
	now := time.Now().UTC()
	content := fmt.Sprintf(
		"# GoCell initial admin credential\n"+
			"# Generated at: %s\n"+
			"# Expires at:   %s\n"+
			"# This file is auto-deleted by the cleanup worker.\n"+
			"username=%s\n"+
			"password=%s\n"+
			"expires_at=%d\n",
		now.Format(time.RFC3339),
		p.ExpiresAt.UTC().Format(time.RFC3339),
		p.Username,
		p.Password,
		p.ExpiresAt.Unix(),
	)

	_, err := fmt.Fprint(w, content)
	return err
}
