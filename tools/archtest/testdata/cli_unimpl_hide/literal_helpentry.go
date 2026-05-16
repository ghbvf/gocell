// Fixture for CLI-UNIMPL-HIDE-01 reverse self-check (downstream-Hard).
//
// Hand-written helpEntry composite literals carrying string-literal names
// in both named-field and positional form (blind-spot ledger §2).
// scanHelpEntryNoLiteralName MUST flag both. Not compiled (testdata).
package fixture

type helpEntry struct {
	name string
	desc []string
}

var namedFormHelp = []helpEntry{
	{name: "namedKey", desc: []string{"hand-written, must be flagged"}},
}

var positionalFormHelp = []helpEntry{
	{"positional0", []string{"hand-written, must be flagged"}},
}
