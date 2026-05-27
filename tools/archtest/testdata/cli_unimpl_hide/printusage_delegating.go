// Fixture for CLI-TOPLEVEL-HELP-REGISTRY-01 reverse self-check
// (downstream form-uniqueness — must NOT flag).
//
// The compliant shape: PrintUsage's body is exactly one statement, a
// renderTopHelp(commands, …) delegation. scanPrintUsageDerived MUST pass
// it (0 diagnostics), so the detector cannot regress to flagging
// everything. renderTopHelp / commands are undeclared here — parseFixture
// is pure AST (SkipObjectResolution), not compiled (testdata).
package fixture

func PrintUsage() {
	renderTopHelp(commands, "Run 'gocell <command> -h' for full flag help on a sub-command.")
}
