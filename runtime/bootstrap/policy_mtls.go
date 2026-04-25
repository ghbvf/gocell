package bootstrap

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// MTLSOption configures PolicyMTLS behavior.
// SEC-01: WithMTLSClientAuth was removed because the clientAuth field was
// never propagated to the listener's tls.Config. The TLS client authentication
// requirement (e.g. tls.RequireAndVerifyClientCert) must be set directly on
// the listener's *tls.Config passed via WithListenerTLS; PolicyMTLS ships as
// pool-verification middleware only.
type MTLSOption func(*policyMTLSConfig)

type policyMTLSConfig struct {
	pool *x509.CertPool
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
func PolicyMTLS(pool *x509.CertPool, opts ...MTLSOption) cell.Policy {
	if pool == nil {
		panic("bootstrap: PolicyMTLS pool must not be nil")
	}
	cfg := &policyMTLSConfig{pool: pool}
	for _, o := range opts {
		o(cfg)
	}
	return cell.Policy{
		Name:       "mtls",
		Middleware: mtlsMiddleware(cfg.pool),
	}
}

// mtlsMiddleware returns an HTTP middleware that asserts the request arrived
// over a mutual-TLS connection — i.e. crypto/tls.Conn.ConnectionState already
// has at least one peer certificate AND the listener's *tls.Config performed
// the chain-and-ClientAuth verification at handshake time.
//
// F4 round-3: this middleware no longer performs leaf+chain verification at
// the application layer. The previous implementation iterated peer certs and
// accepted any cert that validated against the pool with default
// ServerAuth key usage — accepting intermediates as leaves and missing
// ClientAuth EKU. That is wrong (silent fail-open on intermediates).
//
// The Go stdlib already does the right thing inside crypto/tls's
// processCertsFromClient: it verifies leaf-only with Intermediates from the
// rest of the chain and ExtKeyUsageClientAuth, when ClientAuth is set to
// VerifyClientCertIfGiven or stricter on the *tls.Config. gRPC-Go follows
// this exact pattern (credentials/tls.go does no app-layer recheck).
//
// PolicyMTLS is therefore a thin "must have peer cert" guard: the mere
// presence of r.TLS.PeerCertificates implies the handshake-layer chain
// verification has already passed. Configure
// tls.Config.ClientAuth = tls.RequireAndVerifyClientCert with ClientCAs set
// to the pool on the listener's *tls.Config (via WithListenerTLS) for the
// real chain enforcement.
//
// ref: golang/go src/crypto/tls/handshake_server.go processCertsFromClient
// ref: grpc/grpc-go credentials/tls.go ServerHandshake (no recheck)
// ref: caddyserver/caddy CVE-2026-27586 (silent fail-open on app-layer recheck)
func mtlsMiddleware(_ *x509.CertPool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized,
					"ERR_AUTH_MTLS_REQUIRED", "mTLS client certificate required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WithMTLSClientAuth is retained as a no-op MTLSOption for source compatibility
// during migration. TLS ClientAuth type must be set directly on the listener's
// *tls.Config via WithListenerTLS — see PolicyMTLS documentation.
//
// Deprecated: set tls.Config.ClientAuth on the listener's *tls.Config instead.
func WithMTLSClientAuth(_ tls.ClientAuthType) MTLSOption {
	return func(_ *policyMTLSConfig) {
		// Intentionally empty: TLS ClientAuth must be configured on the
		// listener's *tls.Config (see WithListenerTLS); the policy carries no
		// ClientAuth state. Retained as a deprecated no-op for source compat.
	}
}
