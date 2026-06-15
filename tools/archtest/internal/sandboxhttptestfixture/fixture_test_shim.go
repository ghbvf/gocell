//go:build archtest_fixture

// Package sandboxhttptestfixture is the RED/GREEN corpus for
// SANDBOX-HTTPTEST-TCP-FUNNEL-01.
//
// Two shapes are planted:
//
//   - badBareServer (RED): calls httptest.NewServer directly — the exact form
//     that the archtest rule must report as a violation.
//   - goodNettestServer (GREEN): calls nettest.NewServer — the sanctioned funnel;
//     the archtest rule must NOT report this site.
//
// The fixture is loaded via Run(t, Fixture(FixtureOpts{Tests: true}, ...)) with
// the archtest_fixture build tag. Bypassing the self-check requires editing this
// real source file.
package sandboxhttptestfixture

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/testutil/nettest"
)

// badBareServer plants a bare httptest.NewServer call — the RED violation that
// SANDBOX-HTTPTEST-TCP-FUNNEL-01 must detect and report.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func badBareServer(_ *testing.T, h http.Handler) *httptest.Server {
	return httptest.NewServer(h) // RED: bare call, must be reported
}

// goodNettestServer uses the sanctioned nettest.NewServer funnel — GREEN,
// must NOT be reported by the archtest rule.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func goodNettestServer(t *testing.T, h http.Handler) *httptest.Server {
	return nettest.NewServer(t, h) // GREEN: sanctioned funnel
}
