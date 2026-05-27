//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// localBareCellID is a local var bound to a bare string literal — using
// it at a cell-id position is the Ident→BasicLit chain bypass that A1
// must reject.
var localBareCellID = "bareidentchain"

// BadIdentChain uses an Ident bound to a bare BasicLit at the map key
// position — must be flagged by A1 (callsite identity is not a
// metadatatest reference, even though EvaluateConstString would resolve
// it).
var BadIdentChain = &metadata.ProjectMeta{
	Cells: map[string]*metadata.CellMeta{
		localBareCellID: {
			ID:               localBareCellID,
			Type:             "core",
			ConsistencyLevel: "L1",
		},
	},
}
