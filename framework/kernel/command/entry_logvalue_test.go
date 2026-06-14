package command

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEntry_LogValue_RedactsPayload asserts that Entry.LogValue replaces the
// raw Payload bytes with a `<REDACTED bytes=N>` marker so accidental log
// emission cannot leak command negotiation contents (F-S-004).
func TestEntry_LogValue_RedactsPayload(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	secret := []byte(`{"token":"sk_live_should_not_leak"}`)
	entry := NewEntry("cmd-7", "dev-42", "reboot", secret, Timeouts{}, now)
	entry.Attempt = 3
	entry.Status = StatusSent

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	logger.Info("entry-emit", slog.Any("entry", &entry))

	output := buf.String()
	require.NotContains(t, output, "sk_live_should_not_leak", "raw Payload must not appear in log output")
	require.Contains(t, output, "<REDACTED bytes=35>", "payload must collapse to byte-count summary")
	require.Contains(t, output, "id=cmd-7", "id should still log")
	require.Contains(t, output, "deviceId=dev-42")
	require.Contains(t, output, "commandType=reboot")
	require.Contains(t, output, "status=sent")
	require.Contains(t, output, "attempt=3")
}

// TestEntry_LogValue_EmptyPayload covers the zero-length edge case so the
// byte-count formatter is exercised at the boundary.
func TestEntry_LogValue_EmptyPayload(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "id-0",
		DeviceID:    "dev-0",
		CommandType: "ping",
		Status:      StatusPending,
	}
	got := entry.LogValue()
	require.Equal(t, slog.KindGroup, got.Kind())

	rendered := slog.GroupValue(got.Group()...).String()
	assert.True(t, strings.Contains(rendered, "<REDACTED bytes=0>"),
		"empty payload should still render the redaction marker, got=%s", rendered)
}
