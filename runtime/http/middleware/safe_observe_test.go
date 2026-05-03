package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
)

// --- existing cases adapted to new signature ---

func TestSafeObserve_PanicDoesNotPropagate(t *testing.T) {
	assert.NotPanics(t, func() {
		safeObserve(slog.Default(), func() {
			panic("kaboom")
		})
	})
}

func TestMetrics_CollectorPanicDoesNotCrash(t *testing.T) {
	// A collector that panics in RecordRequest must not crash the request.
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WithCellIDContext("test-cell")(Recorder(Metrics(&panicCollector{}, clock.Real())(okHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAccessLog_PanicDoesNotCrash(t *testing.T) {
	// Even if slog panics (e.g. custom handler), the request must complete.
	// We test via safeObserve directly since we cannot easily inject a panicking slog.
	assert.NotPanics(t, func() {
		safeObserve(slog.Default(), func() {
			panic("slog boom")
		})
	})
}

// panicCollector implements metrics.Collector but panics on RecordRequest.
type panicCollector struct{}

func (p *panicCollector) RecordRequest(_, _, _ string, _ int, _ float64) {
	panic("collector panic in RecordRequest")
}

// --- new cases for broken logger behavior ---

// brokenHandler.Handle returns a non-nil error, which slog.Logger.Error discards
// by design (slog docs). This handler verifies safeObserve does not propagate
// logger Handle errors. The "real" double-recover coverage is
// TestSafeObserve_BrokenLogger_Handle_Panic, where Handle panics and the inner
// defer recovers.
//
// brokenHandler is a slog.Handler whose Handle method returns a non-nil error.
// Used to verify that safeObserve swallows a logger error without escaping.
type brokenHandler struct {
	slog.Handler // embed to satisfy remaining interface methods via slog.Default().Handler
}

func newBrokenHandler() *brokenHandler {
	return &brokenHandler{Handler: slog.Default().Handler()}
}

func (h *brokenHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *brokenHandler) Handle(_ context.Context, _ slog.Record) error {
	// Return a non-nil error: safeObserve should swallow it silently.
	return errBrokenHandle
}

// panicHandler is a slog.Handler whose Handle method panics.
// Used to verify that the inner double-recover in safeObserve prevents the
// logger panic from escaping to the caller.
type panicHandler struct {
	slog.Handler
}

func newPanicHandler() *panicHandler {
	return &panicHandler{Handler: slog.Default().Handler()}
}

func (h *panicHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *panicHandler) Handle(_ context.Context, _ slog.Record) error {
	panic("logger handle panic")
}

// errBrokenHandle is the sentinel error returned by brokenHandler.Handle.
// Defined as a simple error value to avoid importing errcode for test-internal use.
var errBrokenHandle = errBrokenHandleVal("brokenHandler: Handle returned error")

type errBrokenHandleVal string

func (e errBrokenHandleVal) Error() string { return string(e) }

// TestSafeObserve_BrokenLogger_Handle_Error verifies that when the slog.Handler
// returns a non-nil error from Handle, safeObserve still absorbs the fn panic
// without propagating any panic or error to the caller.
//
// Note: slog.Logger.Error silently discards the error returned by Handler.Handle
// (per slog design — the log call itself has no error return). Consequently,
// this test exercises the path where safeObserve's outer recover fires and logs
// via a broken handler, but the Handle error is never visible to safeObserve.
// The actual double-recover contract (inner defer catching a panicking logger)
// is exercised by TestSafeObserve_BrokenLogger_Handle_Panic.
func TestSafeObserve_BrokenLogger_Handle_Error(t *testing.T) {
	logger := slog.New(newBrokenHandler())

	assert.NotPanics(t, func() {
		safeObserve(logger, func() {
			panic("fn panic with broken logger")
		})
	}, "safeObserve must not panic even when the logger's Handle returns an error")
}

// TestSafeObserve_BrokenLogger_Handle_Panic verifies that when the slog.Handler
// itself panics inside Handle (double-fault scenario), the inner recover in
// safeObserve prevents the logger panic from escaping to the caller.
//
// Design choice: safeObserve uses a double-layer recover (inner anonymous func
// with its own defer+recover wraps the log call) so that even a panicking logger
// cannot kill the process. This test verifies that contract holds.
func TestSafeObserve_BrokenLogger_Handle_Panic(t *testing.T) {
	logger := slog.New(newPanicHandler())

	assert.NotPanics(t, func() {
		safeObserve(logger, func() {
			panic("fn panic with panicking logger")
		})
	}, "safeObserve must not panic even when the logger's Handle itself panics (double-layer recover)")
}

// TestSafeObserve_NilLogger_FallsBackToDefault verifies that passing nil as
// the logger causes safeObserve to fall back to slog.Default() without
// panicking.
func TestSafeObserve_NilLogger_FallsBackToDefault(t *testing.T) {
	assert.NotPanics(t, func() {
		safeObserve(nil, func() {
			panic("panic with nil logger")
		})
	}, "safeObserve(nil, fn) must not panic — nil logger falls back to slog.Default()")
}
