package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/clock"
	khealthz "github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
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
	asm := assembly.New(clock.Real(), assembly.Config{ID: "test-acc", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	agg := newAgg(clock.Real())
	h := New(asm, agg, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, agg.Register(khealthz.NewProbe("db", func(_ context.Context) error {
		return errors.New("connection refused password=secret")
	})))

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
	// "drop ratio exceeded" is unchanged: LogValue does NOT redact — redaction
	// happens upstream in the newRedactedErrorMsg funnel (exercised here, the
	// input has no key=value secret to mask). This asserts the LogValue
	// serialization shape, not the redaction contract.
	assert.Equal(t, "drop ratio exceeded", got["error_msg"])
}

// TestLogDiagnostics_EmitsGroupWithSnakeCaseViaJSONHandler is the end-to-end
// integration test that proves the architectural fix (round-5): logDiagnostics
// uses slog.Group("dependencies", slog.Any(name, entry)...), and JSON handler
// emits the dep payload with snake_case fields by calling LogValue during
// resolve. Pre-round-5 this would have emitted "dependencies":{"db":{}}
// because slog.Any(map) bypassed LogValue and json.Marshal can't see
// unexported fields.
//
// R5 (#942): assertions round-trip the handler output through json.Unmarshal
// and navigate the nested object — not substring Contains — so the test proves
// the *handler-serialized* shape (dependencies.db.{status,duration_ms,error_msg}),
// not merely that the right attrs were injected.
func TestLogDiagnostics_EmitsGroupWithSnakeCaseViaJSONHandler(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	asm := assembly.New(clock.Real(), assembly.Config{ID: "test-json", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	agg := newAgg(clock.Real())
	h := New(asm, agg, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, agg.Register(khealthz.NewProbe("db", func(_ context.Context) error {
		return errors.New("connection refused")
	})))
	require.NoError(t, agg.Register(khealthz.NewProbe("cache", func(_ context.Context) error {
		return nil // healthy — proves error_msg is "" not omitted
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set(VerboseAuthHeader, testVerboseToken)
	h.ReadyzHandler().ServeHTTP(rec, req)

	rec503 := findJSONRecord(t, buf.String(), "readyz unhealthy")

	deps, ok := rec503["dependencies"].(map[string]any)
	require.True(t, ok, "dependencies must round-trip to a JSON object (slog.Group), got %T", rec503["dependencies"])

	db, ok := deps["db"].(map[string]any)
	require.True(t, ok, "db dep must be a nested object (LogValue GroupValue), got %T", deps["db"])
	assert.Equal(t, "unhealthy", db["status"])
	_, hasDur := db["duration_ms"]
	assert.True(t, hasDur, "db dep must carry duration_ms")
	assert.Equal(t, "connection refused", db["error_msg"])

	cache, ok := deps["cache"].(map[string]any)
	require.True(t, ok, "cache dep must be a nested object")
	assert.Equal(t, "healthy", cache["status"])
	assert.Equal(t, "", cache["error_msg"], "healthy probe error_msg must be empty string, not omitted")

	// Negative: round-4 bug shape (empty object) and the unexported-field
	// fallback shape (CamelCase keys) must never appear.
	out := buf.String()
	assert.NotContains(t, out, `"db":{}`)
	assert.NotContains(t, out, `"Status"`)
	assert.NotContains(t, out, `"DurationMs"`)
	assert.NotContains(t, out, `"ErrorMsg"`)
}

// TestLogDiagnostics_PropagatesRequestCtx is the R2 (#942) regression guard:
// logDiagnostics must thread the request context — carrying request_id /
// trace_id / correlation_id — into slog, not context.Background(). The
// framework's contextHandler injects those correlation fields from ctx; with
// context.Background() they are silently dropped and operators lose the link
// between a 503/degraded diagnostic record and the request that triggered it.
//
// The capture handler records the ctx handed to Handle; we assert the injected
// correlation values survive the writeTo → logDiagnostics → slog.Log hop.
// Against the pre-fix code (slog.Log(context.Background(), ...)) the captured
// ctx is Background and the *From lookups miss → RED.
//
// Both writeTo branches are covered: unhealthy (Warn, 503, msg "readyz
// unhealthy") and degraded (Info, 200, msg "readyz degraded") — each calls
// logDiagnostics with the request ctx, so both must carry the correlation
// fields.
func TestLogDiagnostics_PropagatesRequestCtx(t *testing.T) {
	const (
		wantReqID   = "req-abc-123"
		wantTraceID = "trace-xyz-789"
		wantCorrID  = "corr-456"
	)
	tests := []struct {
		name     string
		probeErr error
		wantCode int
		wantMsg  string
	}{
		{
			name:     "unhealthy path (Warn/503)",
			probeErr: errors.New("connection refused"),
			wantCode: http.StatusServiceUnavailable,
			wantMsg:  "readyz unhealthy",
		},
		{
			name:     "degraded path (Info/200)",
			probeErr: fmt.Errorf("soft degradation: %w", outbox.ErrDegraded),
			wantCode: http.StatusOK,
			wantMsg:  "readyz degraded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asm := assembly.New(clock.Real(), assembly.Config{ID: "test-ctx", DurabilityMode: outbox.DurabilityDemo})
			require.NoError(t, asm.Start(context.Background()))
			t.Cleanup(func() { _ = asm.Stop(context.Background()) })

			agg := newAgg(clock.Real())
			h := New(asm, agg, clock.Real())
			h.SetVerboseToken(testVerboseToken)
			require.NoError(t, agg.Register(khealthz.NewProbe("db", func(_ context.Context) error {
				return tt.probeErr
			})))

			capture := withSlogCapture(t)

			// Inject all three correlation fields directly into the request ctx
			// to prove the threading MECHANISM (whatever is in ctx reaches slog).
			// trace_id is included only to exercise the mechanism — in production
			// the probe endpoints do NOT carry trace_id (DefaultProbeFilter skips
			// tracing for /readyz); request_id + correlation_id are the fields a
			// real readyz record carries (RequestID middleware). See #942 R2.
			ctx := ctxkeys.WithRequestID(context.Background(), wantReqID)
			ctx = ctxkeys.WithTraceID(ctx, wantTraceID)
			ctx = ctxkeys.WithCorrelationID(ctx, wantCorrID)

			rec := httptest.NewRecorder()
			req := newVerboseRequest("/readyz?verbose=true").WithContext(ctx)
			h.ReadyzHandler().ServeHTTP(rec, req)
			require.Equal(t, tt.wantCode, rec.Code)

			gotCtx, ok := capture.recordCtx(tt.wantMsg)
			require.Truef(t, ok, "must capture a %q slog record", tt.wantMsg)

			reqID, _ := ctxkeys.RequestIDFrom(gotCtx)
			assert.Equal(t, wantReqID, reqID, "logDiagnostics must pass the request ctx (request_id) to slog, not context.Background()")
			traceID, _ := ctxkeys.TraceIDFrom(gotCtx)
			assert.Equal(t, wantTraceID, traceID, "trace_id must survive the slog hop")
			corrID, _ := ctxkeys.CorrelationIDFrom(gotCtx)
			assert.Equal(t, wantCorrID, corrID, "correlation_id must survive the slog hop")
		})
	}
}

// TestLogDiagnostics_TextHandlerRoundTrip is the R5 (#942) coverage lock for
// the default text / logfmt handler — the prior contract test only exercised
// the JSON handler. It round-trips the text-handler output through a logfmt
// parser (not substring Contains) so the cross-handler snake_case contract
// (slog.Group + LogValuer) is proven for the key=value format too.
//
// It also pins the exact quoting behavior the docs/ops/readyz.md runbook
// cookbook depends on: a redacted/space-containing error_msg is quoted, an
// empty error_msg is "" — operators' grep patterns must match both.
func TestLogDiagnostics_TextHandlerRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	asm := assembly.New(clock.Real(), assembly.Config{ID: "test-text", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	agg := newAgg(clock.Real())
	h := New(asm, agg, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, agg.Register(khealthz.NewProbe("db", func(_ context.Context) error {
		return errors.New("dial failed password=hunter2")
	})))
	require.NoError(t, agg.Register(khealthz.NewProbe("cache", func(_ context.Context) error {
		return nil // healthy
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set(VerboseAuthHeader, testVerboseToken)
	h.ReadyzHandler().ServeHTTP(rec, req)

	line := findLogfmtLine(t, buf.String(), "readyz unhealthy")
	kv := parseLogfmtLine(line)

	assert.Equal(t, "unhealthy", kv["dependencies.db.status"], "text handler must emit snake_case dotted group keys")
	_, hasDur := kv["dependencies.db.duration_ms"]
	assert.True(t, hasDur, "db dep must carry duration_ms in text output")
	assert.Contains(t, kv["dependencies.db.error_msg"], "<REDACTED>", "secret must be redacted in text output")
	assert.NotContains(t, kv["dependencies.db.error_msg"], "hunter2", "raw secret must not appear")

	assert.Equal(t, "healthy", kv["dependencies.cache.status"])
	assert.Equal(t, "", kv["dependencies.cache.error_msg"], "healthy probe error_msg must round-trip to empty string")
}

// findJSONRecord scans newline-delimited slog JSON output for the first record
// whose "msg" equals msg and returns it as a parsed map. Fails the test if no
// such record is present.
func findJSONRecord(t *testing.T, out, msg string) map[string]any {
	t.Helper()
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if ln == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			continue
		}
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no JSON slog record with msg=%q in output:\n%s", msg, out)
	return nil
}

// findLogfmtLine returns the first newline-delimited text-handler line whose
// msg field equals msg. slog quotes msg values containing spaces, so we match
// on the quoted form. Fails the test if absent.
func findLogfmtLine(t *testing.T, out, msg string) string {
	t.Helper()
	want := `msg="` + msg + `"`
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(ln, want) {
			return ln
		}
	}
	t.Fatalf("no text slog line with %s in output:\n%s", want, out)
	return ""
}

// parseLogfmtLine tokenizes a single slog text-handler (logfmt) line into a
// key→value map, handling both bare values (key=value) and double-quoted
// values (key="value with spaces"). Quoted values have their surrounding
// quotes stripped and \" / \\ unescaped. slog.TextHandler serializes a
// slog.Group as flat dotted keys (group.sub=val) — a documented log/slog
// convention, not an internal detail — so dependency fields decode as
// dependencies.<probe>.<field>. This is a round-trip decoder: it reverses what
// slog.NewTextHandler emitted, so assertions run against decoded values rather
// than raw substrings.
//
// Known limits (sufficient for the single-line readyz record under test): one
// line only (caller pre-selects the line); no \n inside values; first
// occurrence wins on duplicate keys.
func parseLogfmtLine(line string) map[string]string {
	m := make(map[string]string)
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' {
			continue // token without '=' — skip
		}
		key := line[start:i]
		i++ // consume '='
		var val string
		if i < len(line) && line[i] == '"' {
			i++
			var sb strings.Builder
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				}
				sb.WriteByte(line[i])
				i++
			}
			i++ // consume closing quote
			val = sb.String()
		} else {
			vs := i
			for i < len(line) && line[i] != ' ' {
				i++
			}
			val = line[vs:i]
		}
		m[key] = val
	}
	return m
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
