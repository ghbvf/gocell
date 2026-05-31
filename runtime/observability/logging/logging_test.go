package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
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
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-123")
	ctx = kctxkeys.WithCellID(ctx, "accesscore")
	ctx = kctxkeys.WithContractID(ctx, "http.auth.login.v1")

	logger.InfoContext(ctx, "test message", slog.String("extra", "value"))

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "test message", entry["msg"])
	assert.Equal(t, "trace-abc", entry["trace_id"])
	assert.Equal(t, "span-xyz", entry["span_id"])
	assert.Equal(t, "req-123", entry["request_id"])
	assert.Equal(t, "corr-123", entry["correlation_id"])
	assert.Equal(t, "accesscore", entry["cell_id"])
	assert.Equal(t, "http.auth.login.v1", entry["contract_id"])
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
	assert.NotContains(t, entry, "correlation_id")
	assert.NotContains(t, entry, "cell_id")
	assert.NotContains(t, entry, "contract_id")
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

// TestContextHandler_ContractID_EmptyValueSkipped — ContractID set to empty
// string must not emit a contract_id field (matches every other ctx-key in
// extractContextAttrs which guards v \!= "").
func TestContextHandler_ContractID_EmptyValueSkipped(t *testing.T) {
	var buf bytes.Buffer
	handler := NewHandler(Options{
		Level:  slog.LevelInfo,
		Format: FormatJSON,
		Writer: &buf,
	})
	logger := slog.New(handler)

	ctx := kctxkeys.WithContractID(context.Background(), "")
	logger.InfoContext(ctx, "empty contract id")

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)
	assert.NotContains(t, entry, "contract_id")
}

// ---------------------------------------------------------------------------
// B1 — sink-side redaction tests
// All tests use an internal white-box handler via NewHandler + bytes.Buffer.
// ---------------------------------------------------------------------------

const mask = "<REDACTED>"

func newTestHandler(buf *bytes.Buffer) slog.Handler {
	return NewHandler(Options{
		Level:  slog.LevelDebug,
		Format: FormatJSON,
		Writer: buf,
	})
}

func parseEntry(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	return entry
}

// TestRedactingHandler_FreeFormAttrStringMask verifies that a slog.String attr
// containing a sensitive key=value pattern is masked in the JSON output.
func TestRedactingHandler_FreeFormAttrStringMask(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	logger.Info("login error", slog.String("note", "password=hunter2"))

	entry := parseEntry(t, &buf)
	note, ok := entry["note"].(string)
	require.True(t, ok, "note field must be a string")
	assert.Contains(t, note, mask)
	assert.NotContains(t, note, "hunter2")
}

// TestRedactingHandler_KeyAwareMask verifies that a slog attr whose key is a
// sensitive field name gets its value replaced with Mask regardless of content.
func TestRedactingHandler_KeyAwareMask(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	logger.Info("auth", slog.String("password", "x"))

	entry := parseEntry(t, &buf)
	assert.Equal(t, mask, entry["password"])
}

// TestRedactingHandler_SlogAnyRedacted verifies that slog.Any with a struct or
// error carrying sensitive text is redacted after stringify.
func TestRedactingHandler_SlogAnyRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	type event struct{ Token string }
	logger.Info("event", slog.Any("payload", event{Token: "password=hunter2"}))

	out := buf.String()
	assert.Contains(t, out, mask)
}

// TestRedactingHandler_GroupAttrRedacted verifies that sensitive attrs nested
// inside a slog.Group are recursively redacted.
func TestRedactingHandler_GroupAttrRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	logger.Info("req",
		slog.Group("headers",
			slog.String("authorization", "Bearer abc"),
			slog.String("x-trace-id", "trace-1"),
		),
	)

	out := buf.String()
	assert.Contains(t, out, mask)
	assert.NotContains(t, out, "Bearer abc")
	// benign field should survive
	assert.Contains(t, out, "trace-1")
}

// TestRedactingHandler_MessageRedacted verifies that a sensitive pattern
// embedded in the log message is masked.
func TestRedactingHandler_MessageRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	logger.Error("dsn=postgres://u:p@host/db connect failed")

	entry := parseEntry(t, &buf)
	msg, ok := entry["msg"].(string)
	require.True(t, ok)
	assert.Contains(t, msg, mask)
	assert.NotContains(t, msg, "postgres://u:p@host/db")
}

// TestRedactingHandler_WithAttrsPreBound verifies that attrs pre-bound via
// logger.With are redacted at bind time — this is the key WithAttrs path.
func TestRedactingHandler_WithAttrsPreBound(t *testing.T) {
	var buf bytes.Buffer
	base := newTestHandler(&buf)
	logger := slog.New(base).With("authorization", "Bearer abc")

	logger.Info("x")

	entry := parseEntry(t, &buf)
	authVal, ok := entry["authorization"].(string)
	require.True(t, ok, "authorization field must be present")
	assert.Equal(t, mask, authVal, "pre-bound sensitive attr must be masked")
	assert.NotContains(t, authVal, "Bearer abc")
}

// TestRedactingHandler_ContextFieldsStillInjected verifies that context-
// injected fields (trace_id etc.) still appear in the output after redaction.
func TestRedactingHandler_ContextFieldsStillInjected(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	ctx := ctxkeys.WithTraceID(context.Background(), "trace-xyz")
	logger.InfoContext(ctx, "ok")

	entry := parseEntry(t, &buf)
	assert.Equal(t, "trace-xyz", entry["trace_id"])
}

// TestRedactingHandler_WithGroupAttrRedacted verifies that sensitive attrs nested
// inside a WithGroup-created group are still redacted. This tests the WithGroup
// godoc guarantee: "the group prefix does not bypass attr-level redaction".
func TestRedactingHandler_WithGroupAttrRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewHandler(Options{
		Level:  slog.LevelDebug,
		Format: FormatJSON,
		Writer: &buf,
	})).WithGroup("auth")

	logger.Info("req", slog.String("password", "x"))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))

	authGroup, ok := entry["auth"].(map[string]any)
	require.True(t, ok, "auth group must be present in JSON output")
	assert.Equal(t, mask, authGroup["password"],
		"password attr inside WithGroup must be redacted")
}

// TestRedactingHandler_BenignAttrUnchanged verifies that non-sensitive attrs
// pass through without modification.
func TestRedactingHandler_BenignAttrUnchanged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestHandler(&buf))

	logger.Info("ping", slog.String("host", "example.com"), slog.Int("port", 8080))

	entry := parseEntry(t, &buf)
	assert.Equal(t, "example.com", entry["host"])
	assert.EqualValues(t, 8080, entry["port"])
	assert.False(t, strings.Contains(buf.String(), mask))
}
