//go:build segregation_check

// bad_jwt_groupauth.go — compile-error fixture for AUTH-PLAN segregation test.
//
// This file intentionally does NOT compile under the `segregation_check` build
// tag. It proves that AuthJWT does NOT implement cell.GroupAuth (the compile-
// time segregation invariant). The archtest
// TestAuthPlan_Segregation_FixtureVerifiesCompileError invokes
// `go vet -tags=segregation_check` against this directory and asserts that the
// type-checker reports the bad assignment as an error.
//
// Without the build tag, the file is excluded from the normal build so the
// repository compiles cleanly. Do NOT remove the build constraint.

package celltest_segregation

import "github.com/ghbvf/gocell/kernel/cell"

// This assignment intentionally fails to compile:
// AuthJWT does not implement GroupAuth (missing method groupAuthOK).
var _ cell.GroupAuth = cell.AuthJWT{}
