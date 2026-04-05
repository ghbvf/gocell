package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextHandler_JSON_WithAllContextValues(t *testing.T) {
	var buf bytes.Buffer
	handler := NewHandler(Options{
		Level:  slog.LevelInfo,
		Format: FormatJSON,
		Writer: &buf,
	})
	logger := slog.New(handler)

	ctx := context.Background()
	ctx = ctxkeys.WithTraceID(ctx, "trace-abc")
	ctx = ctxkeys.WithSpanID(ctx, "span-xyz")
	ctx = ctxkeys.WithRequestID(ctx, "req-123")
	ctx = ctxkeys.WithCellID(ctx, "access-core")

	logger.InfoContext(ctx, "test message", slog.String("extra", "value"))

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "test message", entry["msg"])
	assert.Equal(t, "trace-abc", entry["trace_id"])
	assert.Equal(t, "span-xyz", entry["span_id"])
	assert.Equal(t, "req-123", entry["request_id"])
	assert.Equal(t, "access-core", entry["cell_id"])
	assert.Equal(t, "value", entry["extra"])
}

func TestContextHandler_NoContextValues(t *testing.T) {
	var buf bytes.Buffer
	handler := NewHandler(Options{
		Level:  slog.LevelInfo,
		Format: FormatJSON,
		Writer: &buf,
	})
	logger := slog.New(handler)

	logger.Info("plain message")

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "plain message", entry["msg"])
	// Context fields should be absent.
	assert.NotContains(t, entry, "trace_id")
	assert.NotContains(t, entry, "span_id")
	assert.NotContains(t, entry, "request_id")
	assert.NotContains(t, entry, "cell_id")
}

func TestContextHandler_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	handler := NewHandler(Options{
		Level:  slog.LevelInfo,
		Format: FormatText,
		Writer: &buf,
	})
	logger := slog.New(handler)

	ctx := ctxkeys.WithRequestID(context.Background(), "req-456")
	logger.InfoContext(ctx, "text log")

	output := buf.String()
	assert.Contains(t, output, "text log")
	assert.Contains(t, output, "request_id=req-456")
}

func TestContextHandler_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	handler := NewHandler(Options{
		Level:  slog.LevelWarn,
		Format: FormatJSON,
		Writer: &buf,
	})
	logger := slog.New(handler)

	logger.Info("should not appear")
	assert.Empty(t, buf.String())

	logger.Warn("should appear")
	assert.NotEmpty(t, buf.String())
}

func TestContextHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	handler := NewHandler(Options{
		Level:  slog.LevelInfo,
		Format: FormatJSON,
		Writer: &buf,
	})
	logger := slog.New(handler).With(slog.String("service", "gocell"))

	ctx := ctxkeys.WithTraceID(context.Background(), "trace-1")
	logger.InfoContext(ctx, "with attrs")

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "gocell", entry["service"])
	assert.Equal(t, "trace-1", entry["trace_id"])
}
