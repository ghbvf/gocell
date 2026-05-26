// Fixture for CLI-TOPLEVEL-HELP-REGISTRY-01 reverse self-check
// (downstream form-uniqueness — must flag a non-`commands` registry arg).
//
// PrintUsage's body IS a single renderTopHelp call (the statement shape is
// right) but it renders from a parallel/local slice instead of the canonical
// `commands` registry. isSoleRenderTopHelpCall pins the first argument to the
// identifier `commands`, so scanPrintUsageDerived MUST flag this — proving
// the funnel rejects rendering from a second source, not just hand-printed
// prose. renderTopHelp / localCommands are undeclared here — parseFixture is
// pure AST (SkipObjectResolution), not compiled (testdata).
package fixture

func PrintUsage() {
	renderTopHelp(localCommands, "Run 'gocell <command> -h' for full flag help on a sub-command.")
}
