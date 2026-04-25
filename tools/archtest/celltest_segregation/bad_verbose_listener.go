//go:build segregation_check

// bad_verbose_listener.go — compile-error fixture for AUTH-PLAN segregation test.
//
// This file intentionally does NOT compile under the `segregation_check` build
// tag. It proves that AuthVerboseToken does NOT implement cell.ListenerAuth
// (the compile-time segregation invariant). The archtest
// TestAuthPlan_Segregation_FixtureVerifiesCompileError invokes
// `go vet -tags=segregation_check` against this directory and asserts that the
// type-checker reports the bad assignment as an error.
//
// Without the build tag, the file is excluded from the normal build so the
// repository compiles cleanly. Do NOT remove the build constraint.

package celltest_segregation

import "github.com/ghbvf/gocell/kernel/cell"

// This assignment intentionally fails to compile:
// AuthVerboseToken does not implement ListenerAuth (missing method listenerAuthOK).
var _ cell.ListenerAuth = cell.AuthVerboseToken{}
