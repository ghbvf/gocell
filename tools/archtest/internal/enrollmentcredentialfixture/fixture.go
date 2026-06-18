//go:build archtest_fixture

// Package enrollmentcredentialfixture provides a deliberate RED fixture for the
// ENROLLMENT-CREDENTIAL-MINT-CALLER-01 archtest. Loaded only under the
// archtest_fixture build tag (the tag literal must agree with the unexported
// fixtureBuildTag const in tools/archtest/fixture.go; Go build directives cannot
// reference a Go const). The tag excludes this package from `go build ./...` /
// `go test ./...`, so it never pollutes real-repo scans and is never executed
// (the nil *auth.JWTIssuer is only type-checked, never dereferenced).
//
// # Cases covered
//
// The funnel pins every production (*auth.JWTIssuer).Issue(TokenIntentEnrollment,
// ...) call to the sole sanctioned issuer (EnrollmentCredentialIssuer.Issue). The
// RED case mints an enrollment credential from another package; the GREEN control
// proves the detector is enrollment-intent-specific (it does not flag every Issue).
//
// RED (MUST be flagged):
//   - ForgeEnrollmentCredential — (*auth.JWTIssuer).Issue(auth.TokenIntentEnrollment, ...)
//     outside enrollment_credential.go, bypassing the wrapper's policy enforcement.
//
// GREEN (MUST NOT be flagged):
//   - MakeAccessToken — (*auth.JWTIssuer).Issue(auth.TokenIntentAccess, ...); proves
//     the detector is scoped to the enrollment intent, not "any Issue call".
package enrollmentcredentialfixture

import "github.com/ghbvf/gocell/framework/runtime/auth"

// iss is a nil issuer used only for type-checking; the fixture is never executed.
var iss *auth.JWTIssuer

// ForgeEnrollmentCredential is the RED case: an enrollment credential minted via a
// bare (*auth.JWTIssuer).Issue outside the sanctioned EnrollmentCredentialIssuer.
// The funnel archtest must flag this callsite.
func ForgeEnrollmentCredential() (string, error) {
	return iss.Issue(auth.TokenIntentEnrollment, "forged-device", auth.IssueOptions{
		PrincipalKind: auth.PrincipalKindClaimDevice,
		TenantID:      "11111111-1111-1111-1111-111111111111",
	})
}

// MakeAccessToken is the GREEN control: an ordinary access token, which the
// detector must NOT flag.
func MakeAccessToken() (string, error) {
	return iss.Issue(auth.TokenIntentAccess, "real-user", auth.IssueOptions{})
}
