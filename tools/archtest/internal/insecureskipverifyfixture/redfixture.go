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
// rule forbids (1 hit).
//
//nolint:all // intentional violation for archtest RED fixture
func violateInsecureSkipVerifyLiteral() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // intentional RED fixture
	}
}
