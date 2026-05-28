package middleware

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// MTLS returns the application-layer guard installed on listeners whose
// auth chain contains kernel/auth.AuthMTLS. It does two things and only
// those two:
//
//  1. Rejects requests that did not terminate as TLS or that present no peer
//     certificate, returning a 401 envelope with code ERR_AUTH_UNAUTHORIZED.
//  2. Extracts the leaf peer certificate's Subject, DNS SANs, and URI SANs
//     into a curated PeerIdentity and stores it on the request context via
//     pkg/ctxkeys.WithPeerIdentity. Downstream handlers read it with
//     pkg/ctxkeys.PeerIdentityFrom — they never see *x509.Certificate.
//
// Chain validation (peer cert ↔ trust pool) is performed at handshake time
// by the TLS layer (tls.Config.ClientAuth=RequireAndVerifyClientCert +
// ClientCAs); the middleware is not a re-check.
//
// This middleware is deliberately decoupled from kernel/auth: it inspects
// http.Request.TLS only, takes no auth-plan parameter, and depends only on
// stdlib + pkg/* — keeping the AUTH-PLAN archtest boundary clean.
//
// To enable mTLS on a listener, three pieces must be wired together:
// (1) build the *tls.Config with runtime/http/tlsutil.NewServerMTLSConfig;
// (2) hand it to bootstrap.WithListenerTLS as the listener's TLS config;
// (3) include kernel/auth.AuthMTLS{} in the listener's auth chain.
// This middleware only covers the application-layer presence check and
// identity extraction; without (1) and (2) the handshake layer will not
// enforce peer cert validation.
//
// ref: spiffe/go-spiffe v2/spiffetls/tlsconfig — Authorizer model
// (typed peer-identity surface vs raw *x509.Certificate).
func MTLS() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				attrs := []any{
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				}
				if rid, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
					attrs = append(attrs, slog.String("request_id", rid))
				}
				slog.Warn("mtls rejected: no peer certificate", attrs...)
				httputil.WriteError(r.Context(), w,
					errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
						"mTLS client certificate required"))
				return
			}
			leaf := r.TLS.PeerCertificates[0]
			id := ctxkeys.PeerIdentity{
				Subject:  leaf.Subject,
				DNSNames: leaf.DNSNames,
				URIs:     leaf.URIs,
			}
			ctx := ctxkeys.WithPeerIdentity(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
