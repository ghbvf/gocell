//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/framework/kernel/metadata"

// BadSliceBelongs uses a bare string literal in SliceMeta.BelongsToCell —
// must be flagged by A1 (direct field position).
var BadSliceBelongs = &metadata.SliceMeta{
	ID:               "myslice",
	BelongsToCell:    "bareliteralcell",
	ConsistencyLevel: "L1",
}
