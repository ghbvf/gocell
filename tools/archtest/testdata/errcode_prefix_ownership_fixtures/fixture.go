// Package errcode_prefix_ownership_fixtures is a fixture for
// ERRCODE-PREFIX-OWNERSHIP-01. It intentionally contains two violations:
//
//  1. An unregistered string literal code passed to errcode.New.
//  2. A non-const runtime-assembled Code value passed to errcode.New.
//
// This fixture is scanned in AST-only mode (no packages.Load), so the
// "errcode" local package name is sufficient for the scanner's AST-only
// fallback path.
//
// NOT intended to compile.
package errcode_prefix_ownership_fixtures

import "github.com/ghbvf/gocell/pkg/errcode"

// ViolatesUnregisteredPrefix calls errcode.New with a string literal that
// has no registered prefix owner — triggers the unregistered-prefix diagnostic.
func ViolatesUnregisteredPrefix() error {
	return errcode.New(errcode.KindInvalid, "ERR_UNREGISTEREDBOGUS_NOPE", "unregistered prefix")
}

// ViolatesNonConstMint calls errcode.New with a runtime-assembled Code value —
// triggers the non-const hard-fail diagnostic.
func ViolatesNonConstMint(suffix string) error {
	bogus := "ERR_" + suffix
	return errcode.New(errcode.KindInvalid, errcode.Code(bogus), "non-const mint")
}
