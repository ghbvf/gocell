//go:build archtest_fixture

// Package celltlsfixture contains intentionally-violating call sites against
// the tlsutil mTLS-material constructors banned by CELLTLS-MATERIAL-FUNNEL-01
// (see celltls_material_funnel_test.go).
//
// Gated by the archtest_fixture build tag; production builds never see this
// file. Loaded by TestCellTLSMaterialFunnel_RedFixtureDetected via
// Run(t, archtest.Fixture(...)) (which injects the archtest_fixture tag).
//
// # Forms covered
//
// Both banned constructors are called in qualified-import form (2 hits), plus
// one aliased-import form for NewClientIdentity (1 hit), for 3 total violations.
package celltlsfixture

import (
	"crypto/x509"

	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
	tlsalias "github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
)

// violateNewClientIdentity calls NewClientIdentity outside the sanctioned
// celltls package (1 qualified hit).
//
//nolint:all // intentional violation for archtest RED fixture
func violateNewClientIdentity(certPEM, keyPEM []byte, pool *x509.CertPool) {
	_, _ = tlsutil.NewClientIdentity(certPEM, keyPEM, pool, "example.org")
}

// violateNewServerMTLSConfig calls NewServerMTLSConfig outside the sanctioned
// celltls package (1 qualified hit).
//
//nolint:all // intentional violation for archtest RED fixture
func violateNewServerMTLSConfig(certPEM, keyPEM []byte, pool *x509.CertPool) {
	_, _ = tlsutil.NewServerMTLSConfig(certPEM, keyPEM, pool)
}

// violateAliasedNewClientIdentity calls NewClientIdentity via an import alias
// (1 aliased hit — verifies ResolvePackageRef is alias-safe).
//
//nolint:all // intentional violation for archtest RED fixture
func violateAliasedNewClientIdentity(certPEM, keyPEM []byte, pool *x509.CertPool) {
	_, _ = tlsalias.NewClientIdentity(certPEM, keyPEM, pool, "example.org")
}
