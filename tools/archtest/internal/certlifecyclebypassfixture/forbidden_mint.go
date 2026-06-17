//go:build archtest_fixture

// Package certlifecyclebypassfixture is an intentionally-violating stand-in for a
// cert-lifecycle reconciler that BYPASSES the certsigning.Signer seam by minting
// an IssuedCert directly. It is the RED self-check for
// CERTLIFECYCLE-SIGN-VIA-FUNNEL-01: the mint detector must fire on this
// NewIssuedCert call from a package that is not the sanctioned signer adapter.
// Gated by the archtest_fixture build tag (a hard-coded literal that must agree
// with the unexported fixtureBuildTag const in tools/archtest/fixture.go; Go
// build-directive syntax cannot reference a Go constant).
package certlifecyclebypassfixture

import cs "github.com/ghbvf/gocell/framework/runtime/certsigning"

// VIOLATION: a lifecycle-shaped package minting a certificate directly instead of
// obtaining it through certsigning.Signer.Sign. The empty cs.CertScope{} literal
// compiles (a struct with only unexported fields permits its EMPTY literal); the
// scanner flags the CALL to NewIssuedCert, not the scope literal.
//
//nolint:all // intentional violation for archtest RED fixture
var redLifecycleMint, _ = cs.NewIssuedCert(cs.CertScope{}, nil, nil, 0) //nolint:unused
