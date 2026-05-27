// Package invalid_value_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/A1 value-shape sub-rule.
//
// It declares healthz.ProbeName typed consts whose values pass the
// adapter "_ready" suffix check but fail the same regex/length validator
// that runtime callers run via healthz.NewProbeName. Without the value-shape
// sub-rule, an author could declare a typed const with hyphens, uppercase
// letters, or exceeding the length budget; A1 would silently accept it,
// the wire-illegal value would then survive into golden inventory and only
// fail at runtime when a composed-name constructor re-validates the prefix
// or when the metric backend rejects the label far from this declaration.
//
// DO NOT use this package in production code.
//
// To stage this as a fixture-package archtest run, the file lives in
// adapters/foo so it matches adapterSanctionedPkgs in the synthetic load —
// see decl_bypass_red for the same pattern.
package invalid_value_red

import "github.com/ghbvf/gocell/kernel/healthz"

// VIOLATION A1/value-shape: contains hyphen — fails ^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$.
// The "_ready" suffix check passes, so without value-shape this would slip through.
const HyphenProbe healthz.ProbeName = "bad-hyphen_ready"

// VIOLATION A1/value-shape: uppercase letters — fails the lowercase regex.
const UppercaseProbe healthz.ProbeName = "BadCase_ready"

// VIOLATION A1/value-shape: double underscore — fails the (?:_[a-z0-9]+)* group.
const DoubleUnderProbe healthz.ProbeName = "double__under_ready"
