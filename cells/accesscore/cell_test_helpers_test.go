package accesscore

import "net/http"

// testPassthroughBootstrapAuth is a no-op middleware satisfying the
// WithBootstrapAuth closed-contract requirement. Cell-level tests do not
// exercise per-route Basic Auth — that is covered by setup-slice tests in
// cells/accesscore/slices/setup. Production wiring is constructed in
// cmd/corebundle.access_module.go via runtime/auth.NewBootstrapMiddleware.
func testPassthroughBootstrapAuth(next http.Handler) http.Handler { return next }

// withTestBootstrapAuth returns the standard WithBootstrapAuth option used by
// all cell-level tests; centralised so a future change to the closed-contract
// surface (e.g. swapping Option for a struct field) only touches one site.
func withTestBootstrapAuth() Option { return WithBootstrapAuth(testPassthroughBootstrapAuth) }
