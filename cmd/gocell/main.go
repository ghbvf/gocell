// Command gocell is the GoCell metadata / scaffolding CLI entry point.
//
// All command logic lives in the importable cmd/gocell/app package so that
// smoke tests and higher-level drivers can invoke the dispatcher directly.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ghbvf/gocell/cmd/gocell/app"
)

func main() {
	// SIGINT (Ctrl+C) / SIGTERM (container / systemd stop) cancel ctx,
	// which Dispatch threads into the long-running sub-commands (verify's
	// go test subprocesses, validate's runGit, generate metrics-schema's
	// go/packages walk) so the process stops promptly instead of leaving
	// orphaned child processes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.Dispatch(ctx, os.Args[1:])
	// stop() before os.Exit: os.Exit skips deferred calls, so release the
	// signal handler explicitly while we still run normal code.
	stop()
	os.Exit(code)
}
