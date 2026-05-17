package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
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

// TestSlogDependencyEntry_ZeroValueAccessors verifies the three read-only
// accessor methods on the zero value return zero-value strings/int.
func TestSlogDependencyEntry_ZeroValueAccessors(t *testing.T) {
	var e SlogDependencyEntry
	assert.Equal(t, "", e.Status())
	assert.Equal(t, int64(0), e.DurationMs())
	assert.Equal(t, "", e.ErrorMsg())
}

// TestSlogDependencyEntry_AccessorsViaRealHandler builds a real probe path
// (in-memory assembly + one failing checker) and asserts the produced
// SlogDependencyEntry's accessors return the expected values. White-box test
// (package health) so it can construct via the production funnel without an
// exported testing constructor.
func TestSlogDependencyEntry_AccessorsViaRealHandler(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-acc", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error {
		return errors.New("connection refused password=secret")
	}))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	deps := readyzUnhealthyDeps(t, capture)
	require.Contains(t, deps, "db")
	entry := deps["db"]
	assert.Equal(t, "unhealthy", entry.Status())
	assert.Greater(t, entry.DurationMs(), int64(-1), "duration must be non-negative")
	errMsg := entry.ErrorMsg()
	assert.Contains(t, errMsg, "<REDACTED>", "ErrorMsg must contain redaction mask for password=...")
	assert.NotContains(t, errMsg, "secret", "raw password value must not appear")
}

// TestSlogDependencyEntry_LogValue verifies LogValue emits a GroupValue with
// snake_case attr keys (status / duration_ms / error_msg). The LogValue path
// is what slog handlers call during resolve when each entry is passed via
// slog.Any inside a slog.Group("dependencies", ...) — see logDiagnostics.
func TestSlogDependencyEntry_LogValue(t *testing.T) {
	// Construct via the production funnel — same path as aggregateProbeResults.
	entry := SlogDependencyEntry{
		status:     "degraded",
		durationMs: 42,
		errorMsg:   newRedactedErrorMsg(errors.New("drop ratio exceeded")),
	}
	v := entry.LogValue()
	require.Equal(t, slog.KindGroup, v.Kind(), "LogValue must return GroupValue")

	got := make(map[string]any, 3)
	for _, attr := range v.Group() {
		got[attr.Key] = attr.Value.Any()
	}
	assert.Equal(t, "degraded", got["status"])
	assert.Equal(t, int64(42), got["duration_ms"])
	assert.Equal(t, "drop ratio exceeded", got["error_msg"])
}

// TestLogDiagnostics_EmitsGroupWithSnakeCaseViaJSONHandler is the end-to-end
// integration test that proves the architectural fix (round-5): logDiagnostics
// uses slog.Group("dependencies", slog.Any(name, entry)...), and JSON handler
// emits the dep payload with snake_case fields by calling LogValue during
// resolve. Pre-round-5 this would have emitted "dependencies":{"db":{}}
// because slog.Any(map) bypassed LogValue and json.Marshal can't see
// unexported fields.
func TestLogDiagnostics_EmitsGroupWithSnakeCaseViaJSONHandler(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	asm := assembly.New(assembly.Config{ID: "test-json", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error {
		return errors.New("connection refused")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set(VerboseAuthHeader, testVerboseToken)
	h.ReadyzHandler().ServeHTTP(rec, req)

	out := buf.String()
	// Locate the readyz unhealthy record (last one — preceded by other slog
	// records from probe setup).
	require.Contains(t, out, `"msg":"readyz unhealthy"`)
	require.Contains(t, out, `"dependencies":{`, "dependencies must be a JSON object (slog.Group)")
	require.Contains(t, out, `"db":{`, "db dep must be a sub-object (LogValue GroupValue)")
	assert.Contains(t, out, `"status":"unhealthy"`)
	assert.Contains(t, out, `"duration_ms":`)
	assert.Contains(t, out, `"error_msg":"connection refused"`)
	// Negative: must NOT emit empty objects (round-4 bug shape) or CamelCase
	// fields (the unexported-field fallback shape).
	assert.NotContains(t, out, `"db":{}`)
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

	// Sanity: wire body must not mention "error" or "error_msg".
	assert.False(t, strings.Contains(got, "error"),
		"verboseDependencyEntry JSON serialization must not contain any error field")
}
