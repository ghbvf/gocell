package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseJSONLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
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
