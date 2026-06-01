// Command gocell is the GoCell metadata / scaffolding CLI entry point.
//
// All command logic lives in the importable cmd/gocell/app package so that
// smoke tests and higher-level drivers can invoke the dispatcher directly.
// Signal handling (SIGINT/SIGTERM → ctx cancel + bounded-termination
// watchdog) lives in app.RunWithSignal so its branches are unit-testable
// without real signals; see cmd/gocell/app/signalrun.go.
package main

import (
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/cmd/gocell/app"
	"github.com/ghbvf/gocell/runtime/observability/logging"
)

func main() {
	// Fail-closed sink-side redaction: seal the redacting slog default FIRST,
	// before any log-relevant action, so cmd/gocell's production slog.Warn/Error
	// (e.g. export wire-summary / dep-graph scan failures) are scrubbed
	// (SLOG-HANDLER-SEALED-FUNNEL-01 A3 handwritten segment). cmd/gocell is a
	// governance/codegen CLI, not a runtime binary, so it seals with FormatText
	// (human-readable) rather than FormatJSON — both route through the redacting
	// contextHandler. Tests invoke app.RunWithSignal directly and are unaffected.
	slog.SetDefault(slog.New(logging.NewHandler(logging.Options{Format: logging.FormatText})))
	os.Exit(app.RunWithSignal(os.Args[1:]))
}
