//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// BadAssemblyCells uses bare string literals as elements of
// AssemblyMeta.Cells — each element must be flagged by A1 (slice
// element position).
var BadAssemblyCells = &metadata.AssemblyMeta{
	ID:    "myassembly",
	Cells: []string{"bareliteralcellone", "bareliteralcelltwo"},
}
