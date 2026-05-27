// Package decl_bypass_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/A1.
//
// It declares a healthz.ProbeName typed const in a package that is NOT in the
// sanctioned package set (kernel/healthz, adapters/*, runtime/*, cells/*/healthz_gen.go).
// The archtest scanner must detect this as an A1 declaration-sanction violation.
//
// DO NOT use this package in production code.
package decl_bypass_red

import "github.com/ghbvf/gocell/kernel/healthz"

// VIOLATION A1: ProbeName const declared in a non-sanctioned package.
// Only adapter/framework/cellgen packages may declare ProbeName consts.
// This package (fixturetest/probename_sealed_funnel/decl_bypass_red) is not
// in probeNameSanctionedPkgs and must be flagged by A1.
const RogueProbe healthz.ProbeName = "rogue_probe_ready"
