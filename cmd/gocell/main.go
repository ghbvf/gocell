// Command gocell is the GoCell metadata / scaffolding CLI entry point.
//
// All command logic lives in the importable cmd/gocell/app package so that
// smoke tests and higher-level drivers can invoke the dispatcher directly.
// Signal handling (SIGINT/SIGTERM → ctx cancel + bounded-termination
// watchdog) lives in app.RunWithSignal so its branches are unit-testable
// without real signals; see cmd/gocell/app/signalrun.go.
package main

import (
	"os"

	"github.com/ghbvf/gocell/cmd/gocell/app"
)

func main() {
	os.Exit(app.RunWithSignal(os.Args[1:]))
}
