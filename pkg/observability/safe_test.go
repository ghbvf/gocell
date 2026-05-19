package observability_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/observability"
)

// --- test helpers ---

// bufHandler is a slog.Handler that writes JSON records to a bytes.Buffer.
type bufHandler struct {
	buf  *bytes.Buffer
	base slog.Handler
}

func newBufHandler() (*bufHandler, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &bufHandler{
		buf:  buf,
		base: slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	}, buf
}

func (h *bufHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.base.Enabled(ctx, l)
}

func (h *bufHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.base.Handle(ctx, r)
}

func (h *bufHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &bufHandler{buf: h.buf, base: h.base.WithAttrs(attrs)}
}

func (h *bufHandler) WithGroup(name string) slog.Handler {
	return &bufHandler{buf: h.buf, base: h.base.WithGroup(name)}
}

// errorHandler returns a non-nil error from Handle (slog discards the error
// by design, but it exercises the path where safeObserve logs via a broken
// handler without panicking).
type errorHandler struct{ slog.Handler }

func newErrorHandler() *errorHandler {
	return &errorHandler{Handler: slog.Default().Handler()}
}

func (h *errorHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *errorHandler) Handle(_ context.Context, _ slog.Record) error {
	return errHandleError
}

type sentinelError string

func (e sentinelError) Error() string { return string(e) }

var errHandleError sentinelError = "errorHandler: intentional error"

// panicingHandler panics inside Handle, exercising the double-recover path.
type panicingHandler struct{ slog.Handler }

func newPanicingHandler() *panicingHandler {
	return &panicingHandler{Handler: slog.Default().Handler()}
}

func (h *panicingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *panicingHandler) Handle(_ context.Context, _ slog.Record) error {
	panic("panicingHandler: intentional panic inside Handle")
}

// --- table-driven tests ---

func TestSafeObserve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		logger     func() *slog.Logger
		fn         func()
		wantPanic  bool
		wantLogMsg string // substring expected in captured output; empty = don't check
	}{
		{
			name:      "non-panicking fn runs normally",
			logger:    func() *slog.Logger { return slog.Default() },
			fn:        func() { /* no-op */ },
			wantPanic: false,
		},
		{
			name:       "panicking fn is recovered and logged",
			logger:     func() *slog.Logger { h, _ := newBufHandler(); return slog.New(h) },
			fn:         func() { panic("test panic value") },
			wantPanic:  false,
			wantLogMsg: "observability hook panic",
		},
		{
			name:      "nil logger falls back to slog.Default without panic",
			logger:    func() *slog.Logger { return nil },
			fn:        func() { panic("panic with nil logger") },
			wantPanic: false,
		},
		{
			name:      "logger whose Handle returns error does not cause panic",
			logger:    func() *slog.Logger { return slog.New(newErrorHandler()) },
			fn:        func() { panic("fn panic; logger Handle returns error") },
			wantPanic: false,
		},
		{
			name:      "panicking logger Handler is double-recovered and does not escape",
			logger:    func() *slog.Logger { return slog.New(newPanicingHandler()) },
			fn:        func() { panic("fn panic; logger Handle will also panic") },
			wantPanic: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger := tc.logger()
			assert.NotPanics(t, func() {
				observability.SafeObserve(logger, tc.fn)
			}, "SafeObserve must not let any panic escape to the caller")
		})
	}
}

// TestSafeObserve_PanicLogged verifies that when fn panics, the log record
// emitted by SafeObserve contains both "observability hook panic" and the
// panic value so operators can diagnose the root cause.
func TestSafeObserve_PanicLogged(t *testing.T) {
	t.Parallel()

	h, buf := newBufHandler()
	logger := slog.New(h)

	observability.SafeObserve(logger, func() {
		panic("unique-sentinel-panic-12345")
	})

	out := buf.String()
	require.Contains(t, out, "observability hook panic", "log message must be present")
	require.Contains(t, out, "unique-sentinel-panic-12345", "panic value must appear in log")
}

// TestSafeObserve_NilLogger_FallsBackToDefault verifies nil-logger fallback
// by capturing slog.Default() output via SetDefault, then restoring.
func TestSafeObserve_NilLogger_FallsBackToDefault(t *testing.T) {
	// Swap default logger with a capturing logger.
	h, buf := newBufHandler()
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	observability.SafeObserve(nil, func() {
		panic("nil-logger-fallback-sentinel")
	})

	out := buf.String()
	assert.Contains(t, out, "observability hook panic",
		"nil logger must fall back to slog.Default() and log the panic")
}
