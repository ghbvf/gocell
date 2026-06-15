//go:build archtest_fixture

// Package certsignmintfixture contains an intentionally-violating caller for the
// CERT-SIGN-FUNNEL-01 archtest detector. Gated by the archtest_fixture build tag
// (must agree with the literal value of the unexported fixtureBuildTag const in
// tools/archtest/fixture.go; Go build-directive syntax cannot reference a Go
// constant, so this file hard-codes the tag literal).
//
// It exercises the funnel's DOWNSTREAM half: a package outside the sanctioned
// mint allowlist calling certsigning.NewIssuedCert. (The UPSTREAM half — forging
// an IssuedCert by populated composite literal — is unrepresentable: IssuedCert's
// fields are unexported, so cs.IssuedCert{Field: ...} does not compile. That
// compile-time impossibility is the Hard seal; it cannot be a fixture.)
package certsignmintfixture

import cs "github.com/ghbvf/gocell/framework/runtime/certsigning"

// VIOLATION: a non-allowlisted package minting an IssuedCert via the funnel.
// The zero-value cs.CertScope{} composite literal compiles (a struct with only
// unexported fields permits its EMPTY literal); the scanner flags the CALL to
// NewIssuedCert, not the scope literal.
//
//nolint:all // intentional violation for archtest RED fixture
var redMintCaller, _ = cs.NewIssuedCert(cs.CertScope{}, nil, nil, 0) //nolint:unused
