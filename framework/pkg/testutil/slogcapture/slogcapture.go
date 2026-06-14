// Package slogcapture is the single sanctioned holder of test-scope process-global
// slog mutation.
//
// INVARIANT SLOG-CAPTURE-GLOBAL-FUNNEL-01: raw slog.SetDefault is banned in test
// code. Tests redirect slog.Default() ONLY via InstallDefault, so the one
// process-global mutation site is funnel-guarded here (enforced by
// tools/archtest/slog_capture_global_funnel_test.go).
//
// Why a pkg/testutil holder (not runtime/http/health/healthtest):
//   - kernel/ and pkg/ tests may not import runtime/ (layering), and health's own
//     white-box tests would import-cycle on healthtest. This package lives in pkg/
//     so EVERY layer can import it.
//   - It is stdlib + testing only — it builds NO slog.Handler and defines NO
//     slog.Handler type, so it trips neither SLOG-HANDLER-SEALED-FUNNEL-01 A1
//     (bans slog.New{JSON,Text}Handler outside logging/) nor that rule's
//     Handler-implementation blind-spot check. Construct the handler/logger in the
//     calling _test.go (where slog.New{JSON,Text}Handler is A1-exempt) and pass it
//     to InstallDefault.
//
// Capture-mechanism choice, by component shape:
//   - Components that read slog.Default() (the GoCell sealed-funnel logging model,
//     SLOG-HANDLER-SEALED-FUNNEL-01) are testable only by redirecting the global
//     default — use InstallDefault. Such a test MUST stay serial (no t.Parallel)
//     so it cannot race a sibling that also redirects the default (#1490).
//   - Components that accept an injected *slog.Logger (e.g. eventbus.New(...,
//     WithLogger(l))) and may emit from async goroutines MUST instead capture via
//     healthtest.NewLoggerCapture + injection — no global mutation, safe under
//     parallel. That async+global combination is exactly the #1490 race.
package slogcapture

import (
	"log/slog"
	"testing"
)

// InstallDefault redirects the process-global slog.Default() to logger for the
// duration of t, restoring the previous default via t.Cleanup. It is the ONLY
// sanctioned test-scope slog.SetDefault call site (SLOG-CAPTURE-GLOBAL-FUNNEL-01).
//
// Construct the handler/logger in the caller, e.g.
//
//	buf := sloghelper.NewSyncBuffer()
//	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
//
// Contract: do NOT call t.Parallel() in a test that uses InstallDefault — the
// global redirect would race parallel siblings. For components that accept an
// injected logger, prefer healthtest.NewLoggerCapture (no global mutation).
func InstallDefault(t *testing.T, logger *slog.Logger) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
}
