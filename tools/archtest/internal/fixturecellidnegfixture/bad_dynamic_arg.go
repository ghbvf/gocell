//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// badDynamicArgVar is a string variable (not a compile-time literal) used as
// the argument to metadatatest.NewCellID. A1 must reject this shape because
// the cell-id value is not a BasicLit — a dynamic argument defeats the
// fail-fast guarantees of the typed-builder funnel.
var badDynamicArgVar = "dynamiccell"

// BadDynamicArg constructs a CellMeta whose ID uses NewCellID with a variable
// argument instead of a string literal — must be flagged by A1.
var BadDynamicArg = map[string]*metadata.CellMeta{
	metadatatest.CellIDAccessCore: {
		ID:               metadatatest.NewCellID(badDynamicArgVar),
		Type:             "core",
		ConsistencyLevel: "L1",
	},
}
