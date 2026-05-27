//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// localBareCellID is a local var bound to a bare string literal — using
// it at a cell-id position is the Ident→BasicLit chain bypass that A1
// must reject.
var localBareCellID = "bareidentchain"

// BadIdentChainMapKey uses an Ident at the map key position — must be
// flagged by A1 (map[string]*metadata.CellMeta key position). First of
// two violations expected from this file.
var BadIdentChainMapKey = &metadata.ProjectMeta{
	Cells: map[string]*metadata.CellMeta{
		localBareCellID: {
			ID:               "bareidentchain",
			Type:             "core",
			ConsistencyLevel: "L1",
		},
	},
}

// BadIdentChainFieldID uses an Ident at a direct CellMeta.ID field
// position — must be flagged by A1 (direct field position). Second of
// two violations expected from this file.
var BadIdentChainFieldID = &metadata.CellMeta{
	ID: localBareCellID,
}
