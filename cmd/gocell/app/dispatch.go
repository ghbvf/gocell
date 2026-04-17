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
var commands = map[string]func(args []string) error{
	"validate": runValidate,
	"scaffold": runScaffold,
	"generate": runGenerate,
	"check":    runCheck,
	"verify":   runVerify,
}

// Dispatch runs the gocell sub-command identified by args[0] and returns a
// process exit code (0 on success, 1 on usage / execution errors). Writes
// errors to stderr; does not call os.Exit so callers keep control.
func Dispatch(args []string) int {
	if len(args) < 1 {
		PrintUsage()
		return 1
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		PrintUsage()
		return 1
	}
	if err := cmd(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// PrintUsage writes the top-level gocell usage summary to stdout.
func PrintUsage() {
	fmt.Println("Usage: gocell <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  validate    Validate all metadata (blocking)")
	fmt.Println("  scaffold    Generate new cell/slice/contract/journey")
	fmt.Println("  generate    Generate assembly code and derived files")
	fmt.Println("  check       Run targeted architecture analysis")
	fmt.Println("  verify      Run tests (slice/cell/journey)")
}
