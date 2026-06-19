//go:build archtest_fixture

// Package tlsmaterialfixture contains intentionally-violating call sites against
// crypto/x509.CreateCertificate, banned outside the sanctioned packages by
// TLS-TEST-MATERIAL-FUNNEL-01 (see tls_test_material_funnel_test.go).
//
// Gated by the archtest_fixture build tag; production builds (and the dogfood
// scan, whose tag union deliberately omits archtest_fixture) never see this
// file. Loaded by TestTLSTestMaterialFunnel_RedFixtureDetected via
// Run(t, archtest.Fixture(...)).
//
// # Forms covered
//
// crypto/x509.CreateCertificate is called in qualified-import form (1 hit) plus
// aliased-import form (1 hit, verifying ResolvePackageRef is alias-safe), for 2
// total violations.
package tlsmaterialfixture

import (
	"crypto/rand"
	"crypto/x509"
	x509alias "crypto/x509"
)

// violateCreateCertificate calls x509.CreateCertificate outside any sanctioned
// package (1 qualified hit).
//
//nolint:all // intentional violation for archtest RED fixture
func violateCreateCertificate(tmpl, parent *x509.Certificate, pub, priv any) {
	_, _ = x509.CreateCertificate(rand.Reader, tmpl, parent, pub, priv)
}

// violateAliasedCreateCertificate calls x509.CreateCertificate via an import
// alias (1 aliased hit).
//
//nolint:all // intentional violation for archtest RED fixture
func violateAliasedCreateCertificate(tmpl, parent *x509alias.Certificate, pub, priv any) {
	_, _ = x509alias.CreateCertificate(rand.Reader, tmpl, parent, pub, priv)
}
