//go:build archtest_fixture

// Package insecureskipverifyfixture contains an intentionally-violating
// composite-literal InsecureSkipVerify: true outside the sanctioned
// framework/runtime/http/tlsutil package, for the RED-fixture precision test
// of INSECURE-SKIP-VERIFY-LITERAL-01
// (see insecure_skip_verify_funnel_test.go).
//
// Gated by the archtest_fixture build tag; production builds never see this
// file.
package insecureskipverifyfixture

import "crypto/tls"

// violateInsecureSkipVerifyLiteral builds a tls.Config with
// InsecureSkipVerify: true outside the sanctioned tlsutil package and without a
// compensating VerifyConnection callback — exactly the fail-open pattern the
// rule forbids (1 hit). MinVersion is pinned so the fixture violates ONLY the
// thing INSECURE-SKIP-VERIFY-LITERAL-01 detects (the InsecureSkipVerify literal),
// not the incidental Semgrep missing-ssl-minversion rule — keeping the RED case
// precise and the security gate green.
//
//nolint:all // intentional violation for archtest RED fixture
func violateInsecureSkipVerifyLiteral() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // intentional RED fixture
	}
}
