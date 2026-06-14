//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/framework/kernel/metadata"

// someBareSlice is a pre-built []string var with a bare literal cell-id.
// Used below to test the Ident-typed slice blind spot.
var someBareSlice = []string{"bareliteraljourney"}

// BlindSpotIdentSlice uses an *ast.Ident (someBareSlice) rather than an
// inline []string{...} composite literal for JourneyMeta.Cells. A1 only
// inspects inline CompositeLit nodes; an Ident-typed slice value is
// outside A1 scope and must NOT produce a violation. This file is a
// reverse self-test that documents the known blind spot per
// ai-robust.md §载体决策原则 "强制盲区自检".
var BlindSpotIdentSlice = &metadata.JourneyMeta{
	ID:    "J-blindspot",
	Cells: someBareSlice,
}
