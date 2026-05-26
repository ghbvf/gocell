// Fixture for CLI-TOPLEVEL-HELP-REGISTRY-01 reverse self-check
// (downstream form-uniqueness — must flag).
//
// A PrintUsage that hand-prints the top-level command list instead of
// delegating to renderTopHelp(commands, …) as its sole statement — the
// drift-prone free-form prose that let stale sub-types resurface in
// top-level help. scanPrintUsageDerived MUST flag it. The body exercises
// the bypass forms a fmt.Print*-only blacklist would miss
// (fmt.Fprintln(os.Stdout, …)). Not compiled (testdata).
package fixture

import (
	"fmt"
	"os"
)

func PrintUsage() {
	fmt.Println("Usage: gocell <command> [args]")
	fmt.Fprintln(os.Stdout, "  validate  Validate all metadata")
}
