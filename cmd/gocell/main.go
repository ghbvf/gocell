package main

import (
	"fmt"
	"os"
)

var commands = map[string]func(args []string) error{
	"validate": runValidate,
	"scaffold": runScaffold,
	"generate": runGenerate,
	"check":    runCheck,
	"verify":   runVerify,
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
	if err := cmd(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: gocell <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  validate    Validate all metadata (blocking)")
	fmt.Println("  scaffold    Generate new cell/slice/contract/journey")
	fmt.Println("  generate    Generate assembly code and derived files")
	fmt.Println("  check       Run targeted architecture analysis")
	fmt.Println("  verify      Run tests (slice/cell/journey)")
}
