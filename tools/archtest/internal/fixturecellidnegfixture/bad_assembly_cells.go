//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/framework/kernel/metadata"

// BadAssemblyCells uses bare string literals at the AssemblyCellRef.ID
// position — each must be flagged by A1 (struct-field position; the cell-id
// moved from the slice element to AssemblyCellRef.ID in #1086).
var BadAssemblyCells = &metadata.AssemblyMeta{
	ID: "myassembly",
	Cells: []metadata.AssemblyCellRef{
		{ID: "bareliteralcellone"},
		{ID: "bareliteralcelltwo"},
	},
}
