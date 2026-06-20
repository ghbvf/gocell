//go:build archtest_fixture

// Package estenrollauthfixture is a RED fixture for EST-ENROLL-AUTH-BOUNDARY-01.
// It simulates a device-enrollment front-end that reuses the setup-bootstrap
// credential middleware (auth.NewBootstrapMiddleware / auth.BootstrapCredentials)
// instead of the dedicated enrollment-credential verifier — exactly the
// token-confusion vector the boundary forbids. The forbidden-symbol detector must
// flag the bootstrap reference. It is never imported by production code; it exists
// only so the archtest reverse self-check can prove the detector fires.
//
// Gated behind the archtest_fixture build tag so it is invisible to the
// deviceidentity production scan but loaded by the Fixture() façade.
package estenrollauthfixture

import (
	"net/http"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// ForgeBootstrapEnroll wires the setup-bootstrap middleware onto a would-be
// enroll handler (RED): a freshly provisioned device must authenticate with a
// dedicated enrollment credential, NOT the operator setup-bootstrap basic-auth.
func ForgeBootstrapEnroll(next http.Handler, creds auth.BootstrapCredentials) http.Handler {
	mw := auth.NewBootstrapMiddleware(creds, nil, nil)
	return mw(next)
}
