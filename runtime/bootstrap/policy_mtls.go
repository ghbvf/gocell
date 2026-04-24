package bootstrap

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/go-chi/chi/v5"
)

// mtlsConfig holds options for PolicyMTLS.
type mtlsConfig struct {
	clientAuth tls.ClientAuthType
}

// MTLSOption configures PolicyMTLS behavior.
type MTLSOption func(*mtlsConfig)

// WithMTLSClientAuth sets the TLS client authentication requirement.
// Default is tls.RequireAndVerifyClientCert.
func WithMTLSClientAuth(t tls.ClientAuthType) MTLSOption {
	return func(c *mtlsConfig) {
		c.clientAuth = t
	}
}

// policyMTLS verifies mutual TLS peer certificates against a CA pool.
// Cert rotation is explicitly out of scope (plan-acknowledged); pool is
// loaded once at construction time.
type policyMTLS struct {
	pool *x509.CertPool
	cfg  mtlsConfig
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
// ref: go-kratos/kratos transport/http/server.go — TLS config at server build time.
func PolicyMTLS(pool *x509.CertPool, opts ...MTLSOption) *policyMTLS {
	if pool == nil {
		panic("bootstrap: PolicyMTLS pool must not be nil")
	}
	cfg := mtlsConfig{clientAuth: tls.RequireAndVerifyClientCert}
	for _, o := range opts {
		o(&cfg)
	}
	return &policyMTLS{pool: pool, cfg: cfg}
}
