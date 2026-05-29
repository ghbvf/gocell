package middleware

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/logutil"
)

// MTLS returns the application-layer guard installed on listeners whose
// auth chain contains kernel/auth.AuthMTLS. It does two things and only
// those two:
//
//  1. Rejects requests that did not terminate as TLS or that present no peer
//     certificate, returning a 401 envelope with code ERR_AUTH_UNAUTHORIZED.
//  2. Extracts the leaf peer certificate's Subject, DNS SANs, and URI SANs
//     into a curated, fully-owned PeerIdentity (every slice/pointer is copied,
//     so it never aliases the connection-cached *x509.Certificate) and stores
//     it on the request context via pkg/ctxkeys.WithPeerIdentity. Downstream
//     handlers read it with pkg/ctxkeys.PeerIdentityFrom — they never see
//     *x509.Certificate.
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
					slog.String("method", logutil.Sanitize(r.Method)),
					slog.String("path", logutil.Sanitize(r.URL.Path)),
				}
				if rid, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
					attrs = append(attrs, slog.String("request_id", rid))
				}
				// r.Method / r.URL.Path are gosec-tainted net/http inputs. They are
				// routed through logutil.Sanitize (strips control chars / newlines)
				// and emitted as slog STRUCTURED attributes — slog quotes attr values,
				// so there is no format-string interpolation / log-injection sink.
				// gosec G706 taint analysis cannot clear the taint through a sanitize
				// helper, so suppress at this single call site rather than re-adding a
				// file-level exemption that would also blind future log sites.
				slog.Warn("mtls rejected: no peer certificate", attrs...) //nolint:gosec // G706: see note above (sanitized + structured slog)
				httputil.WriteError(r.Context(), w,
					errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
						"mTLS client certificate required"))
				return
			}
			leaf := r.TLS.PeerCertificates[0]
			ctx := ctxkeys.WithPeerIdentity(r.Context(), ownedPeerIdentity(leaf))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ownedPeerIdentity projects the verified leaf certificate into a PeerIdentity
// whose every slice and pointer is OWNED by the returned value, sharing no
// backing storage with the *x509.Certificate. net/http caches the parsed peer
// certificate on the TLS connection and reuses it across keep-alive requests
// (crypto/tls: "the contents should not be modified"); without this copy a
// downstream handler that mutated id.DNSNames / id.URIs / id.Subject.* would
// corrupt the certificate for every subsequent request on that connection.
//
// ref: kubernetes/apiserver pkg/authentication/request/x509 — converts the
// verified chain into an owned user.Info rather than aliasing the cert.
func ownedPeerIdentity(leaf *x509.Certificate) ctxkeys.PeerIdentity {
	return ctxkeys.PeerIdentity{
		Subject:  clonePKIXName(leaf.Subject),
		DNSNames: slices.Clone(leaf.DNSNames),
		URIs:     cloneURLs(leaf.URIs),
	}
}

// clonePKIXName deep-copies every slice-typed field of a pkix.Name so the
// result shares no backing array with the source. The two scalar fields
// (SerialNumber, CommonName) ride along on the struct assignment. Within
// Names / ExtraNames, the per-element Value (an any holding immutable strings)
// and Type (an OID identifier) are read-only by contract and not separately
// cloned — doing so would be over-engineering.
func clonePKIXName(in pkix.Name) pkix.Name {
	out := in // copies scalars; slice headers are re-pointed below
	out.Country = slices.Clone(in.Country)
	out.Organization = slices.Clone(in.Organization)
	out.OrganizationalUnit = slices.Clone(in.OrganizationalUnit)
	out.Locality = slices.Clone(in.Locality)
	out.Province = slices.Clone(in.Province)
	out.StreetAddress = slices.Clone(in.StreetAddress)
	out.PostalCode = slices.Clone(in.PostalCode)
	out.Names = slices.Clone(in.Names)
	out.ExtraNames = slices.Clone(in.ExtraNames)
	return out
}

// cloneURLs copies the slice and each *url.URL element. Cloning only the slice
// header would still share each *url.URL, letting a handler mutate the cert's
// SAN URLs in place; url.URL's only pointer field (User *url.Userinfo) is
// immutable, so a shallow per-element struct copy fully isolates each URL.
func cloneURLs(in []*url.URL) []*url.URL {
	if in == nil {
		return nil
	}
	out := make([]*url.URL, len(in))
	for i, u := range in {
		if u == nil {
			continue
		}
		cp := *u
		out[i] = &cp
	}
	return out
}
