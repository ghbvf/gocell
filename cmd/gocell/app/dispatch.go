// Package app implements the gocell CLI command dispatch.
//
// It is importable from test packages so that assembly smoke tests and
// higher-level integration drivers can exercise the full command pipeline
// without shelling out to the built binary.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// commands is the top-level command registry — the single source of truth
// for both Dispatch (handler lookup via findSub) and PrintUsage (help
// derived via renderTopHelp). A command cannot be dispatchable without a
// help entry, nor listed in help without a runnable handler; the two truths
// share one slice and cannot drift (INVARIANT CLI-TOPLEVEL-HELP-REGISTRY-01,
// the top-level peer of the verb-tree CLI-UNIMPL-HIDE-01 funnel — see
// subcommand.go). It is kept unexported so callers go through Dispatch,
// which enforces the error/usage contract; tests in this package may
// reference it directly. Black-box tests in the app_test package must go
// through Dispatch.
//
// help[0] carries the description plus a one-line hint of the most-used
// flags; the embedded spacing aligns the flag-hint column across rows.
// Per-command sub-type detail is intentionally NOT listed here — it lives in
// `gocell <command> -h` (derived from each verb's own registry), so the
// top-level surface stays fully registry-derived and cannot drift.
//
// The ctx parameter is the signal-aware context wired in main.go
// (signal.NotifyContext); commands that have a cancelable downstream
// (validate, verify, generate metrics-schema) thread it all the way to
// the go test / go/packages subprocesses. The rest accept it for a
// uniform dispatch signature.
var commands = []subcommand[func(ctx context.Context, args []string) error]{
	{name: "validate", help: []string{"Validate all metadata (blocking)         [--root, --fail-fast, --strict, --format]"}, run: runValidate},
	{name: "scaffold", help: []string{"Generate new cell/slice/contract/journey [--dry-run]"}, run: runScaffold},
	{name: "generate", help: []string{"Generate assembly code and derived files [--id, --module]"}, run: runGenerate},
	{name: "check", help: []string{"Run targeted architecture analysis"}, run: runCheck},
	{name: "verify", help: []string{"Run tests and artifact checks            [--id, --active, --files]"}, run: runVerify},
	{name: "graph", help: []string{"Emit module package dependency graph     [--format, --pattern, --root, --include-tests]"}, run: runGraph},
	{name: "export", help: []string{"Export project catalog (entities + dep graphs) as JSON/YAML"}, run: runExport},
	{
		name: "archtest",
		help: []string{"Run the GoCell archtest suite (alias: verify archtest) [see: gocell verify archtest -h]"},
		run:  runArchtestAlias,
	},
	{name: "version", help: []string{"Print gocell CLI / framework version + compatibility  [--format]"}, run: runVersion},
	{
		name: "derive-service-keys",
		help: []string{
			"Derive per-cell signing+verify subkeys from the HMAC master  [--cell, --callers]",
			"Emits a shell eval-able env block; master secret stays absent from the cell process.",
		},
		run: runDeriveServiceKeys,
	},
}

// Exit codes. Follows the common POSIX convention used by tools like go
// itself: usage/misuse errors are distinct from runtime failures so CI
// scripts can tell "CLI was invoked wrong" apart from "validation failed".
//
// Signal interruption (SIGINT/SIGTERM → ctx canceled) intentionally maps to
// ExitRuntime (1), NOT the shell convention 128+signo (130 for SIGINT).
// gocell is a CI/dev tool whose callers branch on the three-way OK/Runtime/
// Usage contract; a fourth "interrupted" code would force every wrapper
// script to special-case it. Callers that must distinguish an interrupted
// run from a genuine failure match the literal "interrupted" line on stderr
// (emitted by Dispatch on context.Canceled), which is the stable contract.
const (
	ExitOK      = 0 // success
	ExitRuntime = 1 // sub-command returned an error (validation failure, IO, signal interruption, etc.)
	ExitUsage   = 2 // caller passed wrong / unknown / missing arguments
)

// Dispatch runs the gocell sub-command identified by args[0] and returns a
// process exit code: ExitOK (0) on success, ExitUsage (2) when the caller
// invokes gocell incorrectly, ExitRuntime (1) when the sub-command itself
// returns an error. Writes errors to stderr; does not call os.Exit so
// callers keep control.
//
// ctx is the signal-aware context (main.go wires signal.NotifyContext for
// SIGINT/SIGTERM); a sub-command whose ctx is canceled mid-run returns a
// context.Canceled-wrapped error, which Dispatch reports as "interrupted"
// and maps to ExitRuntime (the binary is shutting down, not a usage bug).
//
// Stability: internal. Used by cmd/gocell/main.go and in-tree smoke tests;
// signature may change without notice.
func Dispatch(ctx context.Context, args []string) int {
	if len(args) < 1 {
		PrintUsage()
		return ExitUsage
	}
	cmd, ok := findSub(commands, args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		PrintUsage()
		return ExitUsage
	}
	if err := cmd(ctx, args[1:]); err != nil {
		// `-h` lands here as flag.ErrHelp after the sub-command's flag.Parse
		// already printed its own usage. Treat as a successful help request,
		// not a runtime failure.
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		// SIGINT/SIGTERM cancels ctx; surface a readable "interrupted"
		// line instead of "error: context canceled". Still ExitRuntime —
		// the run did not complete successfully.
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted")
			return ExitRuntime
		}
		fmt.Fprintf(os.Stderr, "error: %s\n", errcode.OperatorString(err))
		return ExitRuntime
	}
	return ExitOK
}

// PrintUsage writes the top-level gocell usage summary to stdout, including a
// one-line hint of the most-used flag per sub-command so `gocell` with no args
// gives discoverable output. Full per-sub-command flag help is available via
// `gocell <sub> -h`.
//
// Stability: internal. Used by cmd/gocell/main.go and in-tree smoke tests;
// signature may change without notice.
func PrintUsage() {
	renderTopHelp(commands, "Run 'gocell <command> -h' for full flag help on a sub-command.")
}
