package initialadmin

import (
	"fmt"
	"io"
	"time"
)

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
func formatPayload(w io.Writer, p credentialPayload) error {
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
