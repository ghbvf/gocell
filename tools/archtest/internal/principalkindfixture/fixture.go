//go:build archtest_fixture

// Package principalkindfixture is the PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01
// reverse self-check corpus. It imports the real runtime/auth.PrincipalKind and
// contains a deliberately NON-exhaustive switch (covers only PrincipalUser, no
// default) so the detector scanPrincipalKindSwitches, pointed at this package,
// must report the missing constants (PrincipalUnknown / PrincipalService /
// PrincipalAnonymous / PrincipalDevice). Bypassing the reverse check requires
// editing this real, type-checked source — not a hand-crafted AST.
//
// Loaded as a real Go package via packages.Load with the archtest_fixture build
// tag (RunTypedFixture); the tag keeps it out of every production scan and
// `go build ./...`.
//
// DO NOT use this package in production code.
package principalkindfixture

import "github.com/ghbvf/gocell/runtime/auth"

// nonExhaustive is the RED case: a switch on auth.PrincipalKind that handles
// only PrincipalUser and omits every other constant, with no default clause.
func nonExhaustive(k auth.PrincipalKind) string {
	switch k {
	case auth.PrincipalUser:
		return "user"
	}
	return ""
}
