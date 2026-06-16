//go:build archtest_fixture

// Package certcustodyfixture contains an intentionally-violating private-key
// holder for the CERT-PRIVATE-KEY-CUSTODY-01 archtest detector. Gated by the
// archtest_fixture build tag (must agree with the literal value of the
// unexported fixtureBuildTag const in tools/archtest/fixture.go; Go
// build-directive syntax cannot reference a Go constant, so this file hard-codes
// the tag literal).
//
// It exercises BOTH downstream Medium legs of CERT-PRIVATE-KEY-CUSTODY-01:
//   - FIELD leg: a package that imports the certsigning seam (so it is in the cert
//     subsystem) but is NOT in the custody allowlist, yet declares a struct field
//     of a crypto private-key type (forbiddenHolder);
//   - GETTER leg: an exported func + an exported method whose result is a
//     private-key type (ForbiddenKeyGetter / KeyVault.Signer) — the export surface
//     the getter ban forbids even in the custody-allowlisted adapter.
//
// The detectors must flag both — the reverse self-check that the GREEN production
// baseline is meaningful.
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
	scope cs.CertScope      // ties this struct to the certsigning seam (importer-scan trigger)
	key   crypto.Signer     // VIOLATION: a private-key-typed field
	raw   *ecdsa.PrivateKey // VIOLATION: a concrete private-key field
}

//nolint:all // keep the fixture types referenced
var _ = forbiddenHolder{}

// ForbiddenKeyGetter hands a signing key across the package boundary via an
// exported FUNCTION result — the getter ban must flag it.
//
//nolint:all // intentional violation for archtest RED fixture
func ForbiddenKeyGetter() crypto.Signer { return nil }

// KeyVault leaks the key via an exported METHOD result — exercises the
// method-scan arm of the getter detector. Its field is a (non-key) CertScope so
// the FIELD scan does not also flag it, isolating the getter leg.
//
//nolint:all // intentional violation for archtest RED fixture
type KeyVault struct{ scope cs.CertScope }

// Signer is an exported method whose result is a private key — forbidden.
//
//nolint:all // intentional violation for archtest RED fixture
func (KeyVault) Signer() crypto.Signer { return nil }
