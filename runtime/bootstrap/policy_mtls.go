package bootstrap

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/go-chi/chi/v5"
)

// MTLSOption configures PolicyMTLS behavior.
// SEC-01: WithMTLSClientAuth was removed because the clientAuth field was
// never propagated to the listener's tls.Config. The TLS client authentication
// requirement (e.g. tls.RequireAndVerifyClientCert) must be set directly on
// the listener's *tls.Config passed via WithListenerTLS; PolicyMTLS ships as
// pool-verification middleware only.
type MTLSOption func(*policyMTLS)

// policyMTLS verifies mutual TLS peer certificates against a CA pool.
// Cert rotation is explicitly out of scope (plan-acknowledged); pool is
// loaded once at construction time.
//
// Note: this policy only validates that at least one peer certificate chains
// to the configured CA pool. To require TLS client certificates at the TLS
// handshake level, set tls.Config.ClientAuth = tls.RequireAndVerifyClientCert
// on the listener's *tls.Config (via WithListenerTLS). PolicyMTLS is a
// defence-in-depth check that runs at the HTTP handler level after TLS terminates.
type policyMTLS struct {
	pool *x509.CertPool
}

func (p *policyMTLS) Describe() string { return "mtls" }

func (p *policyMTLS) Apply(mux *chi.Mux) {
	mux.Use(p.middleware())
}

// middleware returns an http.Handler middleware that verifies at least one
// peer certificate chain validates against the CA pool.
// Requests without TLS info or with no valid chain are rejected with 401.
func (p *policyMTLS) middleware() func(http.Handler) http.Handler {
	pool := p.pool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !verifyMTLSPeerCerts(r, pool) {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized,
					"ERR_AUTH_MTLS_REQUIRED", "mTLS client certificate required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// verifyMTLSPeerCerts returns true if at least one peer certificate in
// r.TLS.PeerCertificates forms a valid chain against pool.
func verifyMTLSPeerCerts(r *http.Request, pool *x509.CertPool) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	opts := x509.VerifyOptions{Roots: pool}
	for _, cert := range r.TLS.PeerCertificates {
		if _, err := cert.Verify(opts); err == nil {
			return true
		}
	}
	return false
}

// PolicyMTLS returns a cell.Policy that verifies mutual TLS peer certificates
// against the provided CA pool.
//
// Fail-fast: pool nil → panic "bootstrap: PolicyMTLS pool must not be nil".
//
// Note: certificate rotation is out of scope for this PR; the pool is loaded
// once at construction time. A future PR may add pool refresh via WatcherOption.
//
// Note: to enforce TLS client certificate requirement at the handshake level,
// set tls.Config.ClientAuth = tls.RequireAndVerifyClientCert on the listener's
// *tls.Config via WithListenerTLS. PolicyMTLS only validates the peer certificate
// chain at the HTTP handler level.
//
// ref: go-kratos/kratos transport/http/server.go — TLS config at server build time.
func PolicyMTLS(pool *x509.CertPool, opts ...MTLSOption) *policyMTLS {
	if pool == nil {
		panic("bootstrap: PolicyMTLS pool must not be nil")
	}
	p := &policyMTLS{pool: pool}
	for _, o := range opts {
		o(p)
	}
	return p
}

// WithMTLSClientAuth is retained as a no-op MTLSOption for source compatibility
// during migration. TLS ClientAuth type must be set directly on the listener's
// *tls.Config via WithListenerTLS — see PolicyMTLS documentation.
//
// Deprecated: set tls.Config.ClientAuth on the listener's *tls.Config instead.
func WithMTLSClientAuth(_ tls.ClientAuthType) MTLSOption {
	return func(_ *policyMTLS) {}
}
