//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// BlindSpotAssign uses an assignment statement (c.ID = "rawassign")
// rather than a CompositeLit to set a cell-id field. A1 scans
// CompositeLit nodes only; assignment statements are outside A1 scope
// and must NOT produce a violation. This file is a reverse self-test
// that documents the known blind spot per ai-robust.md §载体决策原则
// "强制盲区自检".
var BlindSpotAssign = func() *metadata.CellMeta {
	c := &metadata.CellMeta{}
	c.ID = "rawassign"
	return c
}()
