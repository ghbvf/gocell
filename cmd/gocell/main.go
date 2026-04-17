// Command gocell is the GoCell metadata / scaffolding CLI entry point.
//
// All command logic lives in the importable cmd/gocell/app package so that
// smoke tests and higher-level drivers can invoke the dispatcher directly.
package main

import (
	"os"

	"github.com/ghbvf/gocell/cmd/gocell/app"
)

func main() {
	os.Exit(app.Dispatch(os.Args[1:]))
}
