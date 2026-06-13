//go:build archtest_fixture

// Package positive_green is a GREEN fixture for SLOG-CAPTURE-GLOBAL-FUNNEL-01:
// captures via healthtest.CaptureHandler directly (the de-globalized pattern
// healthtest.NewLoggerCapture wraps) — no NewCapture, no slog.SetDefault.
// 0 violations expected.
package positive_green

import (
	"log/slog"

	"github.com/ghbvf/gocell/runtime/http/health/healthtest"
)

// UseCaptureHandlerDirectly builds an isolated capture logger without touching
// the process-global slog.Default().
func UseCaptureHandlerDirectly() *slog.Logger {
	h := &healthtest.CaptureHandler{}
	return slog.New(h)
}
