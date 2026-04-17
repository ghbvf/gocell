// Package app implements the gocell CLI command dispatch.
//
// It is importable from test packages so that assembly smoke tests and
// higher-level integration drivers can exercise the full command pipeline
// without shelling out to the built binary.
package app

import (
	"fmt"
	"os"
)

// commands maps sub-command names to their run functions. It is kept
// unexported so callers go through Dispatch, which enforces the error/usage
// contract; tests in this package may reference it directly.
// Black-box tests in the app_test package must go through Dispatch; direct
// map mutation is not supported.
var commands = map[string]func(args []string) error{
	"validate": runValidate,
	"scaffold": runScaffold,
	"generate": runGenerate,
	"check":    runCheck,
	"verify":   runVerify,
}

// Exit codes. Follows the common POSIX convention used by tools like go
// itself: usage/misuse errors are distinct from runtime failures so CI
// scripts can tell "CLI was invoked wrong" apart from "validation failed".
const (
	ExitOK      = 0 // success
	ExitRuntime = 1 // sub-command returned an error (validation failure, IO, etc.)
	ExitUsage   = 2 // caller passed wrong / unknown / missing arguments
)

// Dispatch runs the gocell sub-command identified by args[0] and returns a
// process exit code: ExitOK (0) on success, ExitUsage (2) when the caller
// invokes gocell incorrectly, ExitRuntime (1) when the sub-command itself
// returns an error. Writes errors to stderr; does not call os.Exit so
// callers keep control.
//
// Stability: internal. Used by cmd/gocell/main.go and in-tree smoke tests;
// signature may change without notice.
func Dispatch(args []string) int {
	if len(args) < 1 {
		PrintUsage()
		return ExitUsage
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		PrintUsage()
		return ExitUsage
	}
	if err := cmd(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
	fmt.Println("Usage: gocell <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  validate    Validate all metadata (blocking)         [--root, --fail-fast]")
	fmt.Println("  scaffold    Generate new cell/slice/contract/journey [--dry-run]")
	fmt.Println("  generate    Generate assembly code and derived files [--id, --module]")
	fmt.Println("  check       Run targeted architecture analysis        [contract-health | slice-coverage | ...]")
	fmt.Println("  verify      Run tests (slice/cell/journey)           [--id, --files]")
	fmt.Println()
	fmt.Println("Run 'gocell <command> -h' for full flag help on a sub-command.")
}
