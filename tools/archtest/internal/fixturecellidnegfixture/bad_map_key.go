//go:build archtest_fixture

// Package fixturecellidnegfixture is the deliberate negative fixture for
// FIXTURE-CELLID-TYPED-BUILDER-01. The bad_*.go files each emit ≥ 1
// violation when scanned by A1; the good_*.go files emit zero. A3 in
// fixture_cellid_typed_builder_test.go loads this package via
// RunTypedFixture and asserts the expected detect/skip behavior.
//
// The archtest_fixture build tag keeps this package out of normal
// builds / tests.
package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// BadMapKey constructs a ProjectMeta with a bare string literal as the
// Cells map key — must be flagged by A1 (map[string]*metadata.CellMeta
// key position).
var BadMapKey = &metadata.ProjectMeta{
	Cells: map[string]*metadata.CellMeta{
		"bareliteralkey": {
			ID:               "bareliteralkey",
			Type:             "core",
			ConsistencyLevel: "L1",
		},
	},
}
