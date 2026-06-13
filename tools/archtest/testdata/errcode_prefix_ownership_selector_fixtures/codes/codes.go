// Package codes holds cross-package string constants referenced by the
// errcode-sentinel red fixtures via SelectorExpr (codes.Unregistered). It
// exists to exercise the ERRCODE-PREFIX-OWNERSHIP-01 Target B residual #2:
// a sentinel whose value is a const SelectorExpr (not a string BasicLit) was
// silently skipped by the AST-only scan before the typed-const-eval fix.
package codes

// Unregistered is an unregistered-prefix code surfaced as an untyped string
// const so it can be assigned to an errcode.Code sentinel in a sibling
// package via a SelectorExpr. The scanner must resolve it through
// EvaluateConstString and flag the owning sentinel.
const Unregistered = "ERR_SELECTORBOGUS_NOPE"

// RegisteredOK resolves to a registered platform whole-code (ERR_INTERNAL).
// A sentinel assigned from it via SelectorExpr must NOT be flagged — this is
// the green anti-vacuity control proving the scan does not flag every
// non-literal sentinel indiscriminately.
const RegisteredOK = "ERR_INTERNAL"
