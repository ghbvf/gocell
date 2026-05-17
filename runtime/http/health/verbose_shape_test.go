package health

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewRedactedErrorMsg_NilReturnsEmpty verifies the nil-input sentinel path.
func TestNewRedactedErrorMsg_NilReturnsEmpty(t *testing.T) {
	got := newRedactedErrorMsg(nil)
	assert.Equal(t, redactedErrorMsg(""), got, "nil err must produce empty sentinel")
}

// TestNewRedactedErrorMsg_NonNilRoutesThroughRedaction verifies non-nil err
// goes through pkg/redaction.RedactString — structured key=value secrets are
// masked, plain text passes through.
func TestNewRedactedErrorMsg_NonNilRoutesThroughRedaction(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantSubs []string // substrings the result must contain
		notSubs  []string // substrings the result must NOT contain
	}{
		{
			name:     "structured secret masked",
			err:      errors.New("dial failed password=hunter2 host=db"),
			wantSubs: []string{"<REDACTED>"},
			notSubs:  []string{"hunter2"},
		},
		{
			name:     "plain text passes through",
			err:      errors.New("connection refused"),
			wantSubs: []string{"connection refused"},
			notSubs:  []string{"<REDACTED>"},
		},
		{
			name:     "authorization header masked",
			err:      errors.New("upstream: Authorization: Bearer eyJhbGc.payload.sig"),
			wantSubs: []string{"<REDACTED>"},
			notSubs:  []string{"eyJhbGc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(newRedactedErrorMsg(tt.err))
			for _, sub := range tt.wantSubs {
				assert.Contains(t, got, sub, "must contain %q", sub)
			}
			for _, sub := range tt.notSubs {
				assert.NotContains(t, got, sub, "must not contain %q", sub)
			}
		})
	}
}

// TestSlogDependencyEntry_Accessors verifies the three read-only accessor
// methods return the underlying field values verbatim.
func TestSlogDependencyEntry_Accessors(t *testing.T) {
	e := NewSlogDependencyEntryForTesting("unhealthy", 123, "connection refused")
	assert.Equal(t, "unhealthy", e.Status())
	assert.Equal(t, int64(123), e.DurationMs())
	assert.Equal(t, "connection refused", e.ErrorMsg())
}

// TestSlogDependencyEntry_ZeroValue verifies the zero value has empty
// accessors — used as the "healthy probe with no error" sentinel shape.
func TestSlogDependencyEntry_ZeroValue(t *testing.T) {
	var e SlogDependencyEntry
	assert.Equal(t, "", e.Status())
	assert.Equal(t, int64(0), e.DurationMs())
	assert.Equal(t, "", e.ErrorMsg())
}

// TestSlogDependencyEntry_LogValue verifies LogValue emits a GroupValue with
// snake_case attr keys (status / duration_ms / error_msg). This is the path
// that fires when SlogDependencyEntry is passed individually as a slog.Any
// argument (rather than wrapped in a map — see struct godoc caveat).
func TestSlogDependencyEntry_LogValue(t *testing.T) {
	e := NewSlogDependencyEntryForTesting("degraded", 42, "drop ratio exceeded")
	v := e.LogValue()
	require.Equal(t, slog.KindGroup, v.Kind(), "LogValue must return GroupValue")

	got := make(map[string]any, 3)
	for _, attr := range v.Group() {
		got[attr.Key] = attr.Value.Any()
	}
	assert.Equal(t, "degraded", got["status"])
	assert.Equal(t, int64(42), got["duration_ms"])
	assert.Equal(t, "drop ratio exceeded", got["error_msg"])
}

// TestSlogDependencyEntry_LogValueEmitsSnakeCaseViaJSONHandler verifies the
// end-to-end JSON-handler path through slog: when an entry is passed
// individually via slog.Any, the JSON output uses snake_case keys courtesy
// of LogValue → GroupValue with snake_case attrs.
func TestSlogDependencyEntry_LogValueEmitsSnakeCaseViaJSONHandler(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	e := NewSlogDependencyEntryForTesting("unhealthy", 99, "deadline exceeded")
	logger.Info("probe", slog.Any("dep", e))

	out := buf.String()
	assert.Contains(t, out, `"status":"unhealthy"`)
	assert.Contains(t, out, `"duration_ms":99`)
	assert.Contains(t, out, `"error_msg":"deadline exceeded"`)
	// Negative check: must NOT use exported Go field names (the unexported
	// fields don't exist for reflection on this code path because LogValue
	// fires first).
	assert.NotContains(t, out, `"Status"`)
	assert.NotContains(t, out, `"DurationMs"`)
	assert.NotContains(t, out, `"ErrorMsg"`)
}

// TestVerboseDependencyEntry_JSONShape verifies the wire shape serializes
// to exactly {"status": ..., "duration_ms": ...} with no error field — the
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 contract from a serialization angle.
func TestVerboseDependencyEntry_JSONShape(t *testing.T) {
	e := verboseDependencyEntry{Status: "healthy", DurationMs: 7}
	buf, err := json.Marshal(e)
	require.NoError(t, err)
	got := string(buf)
	assert.Equal(t, `{"status":"healthy","duration_ms":7}`, got,
		"wire shape must be exactly {status, duration_ms} — no error field")
}
