//go:build archtest_fixture

// Package deviceprincipalmintfixture provides a deliberate RED fixture for the
// DEVICE-PRINCIPAL-MINT-CALLER-01 archtest. Loaded only under the
// archtest_fixture build tag (the tag literal must agree with the unexported
// fixtureBuildTag const in tools/archtest/fixture.go; Go build directives cannot
// reference a Go const). The build tag excludes this package from
// `go build ./...` / `go test ./...`, so it never pollutes real-repo scans.
//
// # Cases covered
//
// The funnel pins every production auth.Principal{Kind: PrincipalDevice}
// construction to the sole sanctioned issuer (mintDevicePrincipal). The RED case
// forges a device principal from another package; the GREEN control proves the
// detector is device-specific (it does not flag every Principal literal).
//
// RED (MUST be flagged):
//   - ForgeDevicePrincipal — auth.Principal{Kind: auth.PrincipalDevice} built
//     outside deviceprincipal.go. (It cannot set the unexported device seal, so
//     it is inert at runtime — the archtest catches it anyway as the in-package
//     file-discipline / intent backstop.)
//
// GREEN (MUST NOT be flagged):
//   - MakeUserPrincipal — auth.Principal{Kind: auth.PrincipalUser}; proves the
//     detector is scoped to PrincipalDevice, not "any Principal literal".
package deviceprincipalmintfixture

import "github.com/ghbvf/gocell/framework/runtime/auth"

// ForgeDevicePrincipal is the RED case: a device-kind principal minted outside
// the sanctioned issuer. The funnel archtest must flag this construction.
func ForgeDevicePrincipal() *auth.Principal {
	return &auth.Principal{Kind: auth.PrincipalDevice, Subject: "forged-device"}
}

// MakeUserPrincipal is the GREEN control: an ordinary user principal literal,
// which the detector must NOT flag.
func MakeUserPrincipal() *auth.Principal {
	return &auth.Principal{Kind: auth.PrincipalUser, Subject: "real-user"}
}
