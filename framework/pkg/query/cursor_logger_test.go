package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
)

func parseJSONLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "failed to parse log line: %s", line)
		out = append(out, m)
	}
	return out
}

func TestLogCursorError_DecodeWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fn := LogCursorError(logger, "auditquery")
	require.NotNil(t, fn)

	ctx := ctxkeys.WithRequestID(context.Background(), "req-123")
	fn(ctx, "decode", fmt.Errorf("invalid base64 encoding"))

	logs := parseJSONLogLines(t, &buf)
	require.Len(t, logs, 1)
	rec := logs[0]

	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "invalid cursor", rec["msg"])
	assert.Equal(t, "auditquery", rec["slice"])
	assert.Equal(t, "decode", rec["reason"])
	assert.Equal(t, "req-123", rec["request_id"])
	assert.NotEmpty(t, rec["error"])
}

func TestLogCursorError_ScopeWithoutRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fn := LogCursorError(logger, "configread")
	fn(context.Background(), "scope", fmt.Errorf("sort scope mismatch"))

	logs := parseJSONLogLines(t, &buf)
	require.Len(t, logs, 1)
	rec := logs[0]

	assert.Equal(t, "configread", rec["slice"])
	assert.Equal(t, "scope", rec["reason"])
	_, hasReqID := rec["request_id"]
	assert.False(t, hasReqID, "request_id must be absent when not in ctx")
}

func TestLogCursorError_NilLogger_ReturnsNil(t *testing.T) {
	fn := LogCursorError(nil, "test")
	assert.Nil(t, fn)
}

// TestLogCursorError_RealCursorInvalidError locks the Error()↔logger contract
// (#1103): after #1103, the diagnostic reason moves to WithInternal (key "_"),
// so the ONLY observability surface is the server-log "error" attr (the result
// of err.Error()). This test passes a real cursorInvalid error (the exact type
// LogCursorError receives in production) and asserts that the logged "error"
// attr contains the reason substring. A bare fmt.Errorf (as previously used)
// would never exercise the [ERR_CURSOR_INVALID] ... server-log format.
func TestLogCursorError_RealCursorInvalidError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fn := LogCursorError(logger, "auditquery")
	require.NotNil(t, fn)

	// Build a real cursorInvalid error — same package, unexported but accessible
	// because this file is package query.
	const reason = `sort scope mismatch: got "x" want "y"`
	realErr := cursorInvalid(reason)

	fn(context.Background(), "scope", realErr)

	logs := parseJSONLogLines(t, &buf)
	require.Len(t, logs, 1)
	rec := logs[0]

	// The logged "error" attr must contain the reason substring so server-side
	// operators can diagnose which cursor dimension mismatched (#1103: this is
	// the ONLY observability surface after the public-detail leak was removed).
	errStr, ok := rec["error"].(string)
	require.True(t, ok, "error attr must be a string")
	assert.Contains(t, errStr, reason,
		"logged error must contain the reason so server-side diagnostics work after #1103")

	// Sanity: the standard slice/reason attrs are still present.
	assert.Equal(t, "auditquery", rec["slice"])
	assert.Equal(t, "scope", rec["reason"])
}
