package accesscore

import (
	"net/http"

	"github.com/ghbvf/gocell/runtime/state/cas"
)

// testPassthroughBootstrapAuth is a no-op middleware satisfying the
// WithBootstrapAuth closed-contract requirement. Cell-level tests do not
// exercise per-route Basic Auth — that is covered by setup-slice tests in
// cells/accesscore/slices/setup. Production wiring is constructed in
// cmd/corebundle.access_module.go via runtime/auth.NewBootstrapMiddleware.
func testPassthroughBootstrapAuth(next http.Handler) http.Handler { return next }

// withTestBootstrapAuth returns the standard WithBootstrapAuth option used by
// all cell-level tests; centralized so a future change to the closed-contract
// surface (e.g. swapping Option for a struct field) only touches one site.
func withTestBootstrapAuth() Option { return WithBootstrapAuth(testPassthroughBootstrapAuth) }

// withTestCASProtocol returns the standard WithCASProtocol option used by all
// cell-level tests that exercise Init(). Mirrors the production wiring in
// cmd/corebundle.access_module.go but constructed inside _test.go so the
// CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest (which skips _test.go) remains
// strict for production paths.
func withTestCASProtocol() Option {
	return WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField("password_version")))
}
