package bootstrap

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// PolicyMTLS returns a cell.Policy that asserts the request arrived over a
// mutual-TLS connection — i.e. r.TLS.PeerCertificates is non-empty.
//
// PolicyMTLS does NOT perform certificate chain verification at the
// application layer. Chain verification, ClientAuth enforcement, and
// CA-pool matching MUST be configured on the listener's *tls.Config via
// WithListenerTLS:
//
//	tlsCfg := &tls.Config{
//	    ClientAuth: tls.RequireAndVerifyClientCert, // mandatory
//	    ClientCAs:  caPool,                          // mandatory
//	}
//	bootstrap.WithListener(cell.InternalListener, addr,
//	    bootstrap.PolicyMTLS(),
//	    bootstrap.WithListenerTLS(tlsCfg))
//
// Bootstrap.phase0 fail-fasts when a listener uses PolicyMTLS without a TLS
// config that has ClientAuth >= tls.VerifyClientCertIfGiven AND a non-nil
// ClientCAs pool — operators cannot accidentally ship PolicyMTLS without
// the handshake-layer chain check. Round-3 finding #11 hardening.
//
// Why no app-layer chain verification:
//   - The Go stdlib's crypto/tls.processCertsFromClient already does the
//     correct chain verification (leaf-only, Intermediates from rest of
//     chain, ExtKeyUsageClientAuth) when ClientAuth is set on tls.Config.
//   - gRPC-Go follows this exact pattern (credentials/tls.go does no
//     app-layer recheck).
//   - Caddy's app-layer recheck triggered CVE-2026-27586 (silent
//     fail-open). Mirror gRPC-Go, not Caddy.
//
// ref: golang/go src/crypto/tls/handshake_server.go processCertsFromClient
// ref: grpc/grpc-go credentials/tls.go ServerHandshake
// ref: go-kratos/kratos transport/http/server.go TLS config at server build
func PolicyMTLS() cell.Policy {
	return cell.Policy{
		Name:       "mtls",
		Middleware: mtlsMiddleware(),
	}
}

// mtlsMiddleware returns the peer-cert-presence guard. The handshake layer
// has already done the chain check (see PolicyMTLS doc), so the middleware
// only needs to assert that the connection terminated as TLS with at least
// one peer cert — anything that reaches here without one is a routing bug.
func mtlsMiddleware() func(http.Handler) http.Handler {
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
