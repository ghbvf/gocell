//go:build archtest_fixture

// Package certcustodyfixture contains an intentionally-violating private-key
// holder for the CERT-PRIVATE-KEY-CUSTODY-01 archtest detector. Gated by the
// archtest_fixture build tag (must agree with the literal value of the
// unexported fixtureBuildTag const in tools/archtest/fixture.go; Go
// build-directive syntax cannot reference a Go constant, so this file hard-codes
// the tag literal).
//
// It exercises the DOWNSTREAM Medium leg: a package that imports the certsigning
// seam (so it is in the cert subsystem) but is NOT in the custody allowlist, yet
// declares a struct field of a crypto private-key type. The detector must flag
// it — the reverse self-check that the GREEN production baseline is meaningful.
package certcustodyfixture

import (
	"crypto"
	"crypto/ecdsa"

	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// forbiddenHolder is a non-allowlisted cert-subsystem struct holding a signing
// key — exactly what custody forbids outside adapters/softca.
//
//nolint:all // intentional violation for archtest RED fixture
type forbiddenHolder struct {
	scope cs.CertScope       // ties this struct to the certsigning seam (importer-scan trigger)
	key   crypto.Signer      // VIOLATION: a private-key-typed field
	raw   *ecdsa.PrivateKey  // VIOLATION: a concrete private-key field
}

//nolint:all // keep the fixture types referenced
var _ = forbiddenHolder{}
