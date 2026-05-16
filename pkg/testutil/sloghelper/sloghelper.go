// Package sloghelper provides test helpers for asserting slog JSON output.
// It avoids false positives from full-string Contains/NotContains by parsing
// each log line individually and matching on specific message substrings.
package sloghelper

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// SyncBuffer is a concurrency-safe slog output sink. A test that installs a
// slog handler over it from the main goroutine while a worker goroutine logs
// concurrently MUST use this instead of a bare bytes.Buffer — slog.Handler
// writes from the worker race with the test's String() read otherwise (the
// race the bare-buffer worker tests previously had under -race).
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// NewSyncBuffer returns a ready-to-use SyncBuffer.
func NewSyncBuffer() *SyncBuffer { return &SyncBuffer{} }

// Write implements io.Writer.
func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns the accumulated log output. Safe to call while concurrent
// writes are in flight.
func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

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
