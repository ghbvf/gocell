// Package sloghelper provides test helpers for asserting slog JSON output.
// It avoids false positives from full-string Contains/NotContains by parsing
// each log line individually and matching on specific message substrings.
package sloghelper

import (
	"bufio"
	"encoding/json"
	"strings"
)

// FindLogEntry scans the JSON log output (one JSON object per line) and returns
// the first parsed line whose "msg" value contains msgSubstr. Returns nil if no
// matching line is found. Malformed JSON lines are silently skipped.
//
// Typical use in tests:
//
//	entry := sloghelper.FindLogEntry(buf.String(), "session not found")
//	require.NotNil(t, entry, "expected a log line about session not found")
//	assert.Equal(t, "WARN", entry["level"])
func FindLogEntry(logOutput, msgSubstr string) map[string]any {
	scanner := bufio.NewScanner(strings.NewReader(logOutput))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		msg, _ := entry["msg"].(string)
		if strings.Contains(msg, msgSubstr) {
			return entry
		}
	}
	return nil
}
