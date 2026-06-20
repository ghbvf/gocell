package auth

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/pkg/logutil"
	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
)

// Cross-bind message constants — MESSAGE-CONST-LITERAL-01.
const (
	msgCrossBindNoPeer     = "mTLS peer certificate required for cross-cell identity binding"
	msgCrossBindNoCertCell = "mTLS peer certificate carries no cell SPIFFE ID (spiffe://<td>/cell/<cell>)"
	msgCrossBindBadCertSet = "mTLS peer certificate carries an invalid cell SPIFFE ID set" +
		" (non-canonical SPIFFE URI or more than one trust domain)"
	msgCrossBindNoPrincipal = "service-token caller principal required for cross-cell identity binding"
	msgCrossBindBadExpected = "cross-cell identity binding: service-token caller cell is not a valid SPIFFE cell token"
	msgCrossBindMismatch    = "service-token caller cell is not in the mTLS peer certificate's cell set"
)

// PeerCellCrossBindMiddleware binds the transport-layer mTLS identity to the
// message-layer service-token identity (#2263, allow-set #2297): it requires the
// service-token caller cell ([Principal.CallerCellID]) — built into a full cell
// SPIFFE ID spiffe://<expectedTrustDomain>/cell/<callerCell> — to be a MEMBER of
// the client certificate's cell set ([spiffeid.CellSet.Contains]). A process may
// host SEVERAL cells, so its workload cert carries every hosted cell's SPIFFE ID;
// the token's caller cell selects WHICH bundled identity is asserted (the token is
// cryptographically pinned per-cell by #2153), and the cert bounds the legitimate
// set. Both trust domain AND cell are checked (not the cell name alone): a peer
// presenting a same-named cell from a DIFFERENT trust domain is rejected, even if
// (via a misissuing CA) its cert chained to the trust pool. On a non-member — or a
// missing peer cert / cell SPIFFE ID / cross-trust-domain cert / service principal
// — it fails closed (403, except a missing peer cert which is 401).
//
// expectedTrustDomain is this listener's own SPIFFE trust domain; bootstrap
// derives it from the listener's server certificate SAN at wiring time (see
// applyListenerAuthChain → serverCertTrustDomain), so peers are required to share
// the server's trust domain without any extra configuration. It must be non-empty
// (a valid trust domain); an empty value makes the expected-ID construction fail
// and every request is rejected (fail-closed).
//
// This is the core of "peer authentication": #2153 (per-cell ProvisionedKeyring)
// already authenticates WHICH cell sent the message at the token layer, and mTLS
// authenticates the connection endpoint at the transport layer; this guard ties
// the two into ONE coherent identity, so a caller cell not bundled in the
// presented certificate cannot pass — and a misprovisioning (cert lacking the
// caller's cell identity) fails closed instead of silently passing. Co-located
// cells sharing one workload cert CAN each assert their own identity by design:
// the process is the trust boundary.
//
// Ordering: it MUST run AFTER both [middleware.MTLS] (which populates
// ctxkeys.PeerIdentity) and [ServiceTokenMiddleware] (which populates the
// Principal). Bootstrap installs it last on the internal listener whenever the
// auth chain contains BOTH AuthMTLS and AuthServiceToken (see
// applyListenerAuthChain), so the ordering holds by construction.
func PeerCellCrossBindMiddleware(expectedTrustDomain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := verifyCrossBind(r, expectedTrustDomain); err != nil {
				denyCrossBind(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// verifyCrossBind returns the fail-closed errcode for a request that does not
// satisfy the cross-bind, or nil when the service-token caller cell
// (spiffe://<expectedTrustDomain>/cell/<callerCell>) is a MEMBER of the peer
// cert's cell set. Extracted from the middleware closure to keep cognitive
// complexity ≤15.
func verifyCrossBind(r *http.Request, expectedTrustDomain string) error {
	peer, ok := ctxkeys.PeerIdentityFrom(r.Context())
	if !ok {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgCrossBindNoPeer)
	}
	certSet, err := spiffeid.CellSetFromURIs(peer.URIs)
	if err != nil {
		// Invalid cell SPIFFE ID set: a non-canonical cell SPIFFE URI
		// (userinfo/port/query/fragment) or cell IDs from ≥2 trust domains. Either
		// is a malformed/forged identity; fail closed and keep the precise cause.
		return errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindBadCertSet, err)
	}
	if certSet.IsEmpty() {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindNoCertCell)
	}
	p, ok := FromContext(r.Context())
	if !ok || p.CallerCellID == "" {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindNoPrincipal)
	}
	// Expected = the caller cell in THIS listener's trust domain. Membership is
	// checked on the full CellID (trust domain + cell) via Contains — never the
	// bare cell string (spiffeid funnel invariant).
	expected, err := spiffeid.ForCell(expectedTrustDomain, p.CallerCellID)
	if err != nil {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindBadExpected,
			errcode.WithInternal(errcode.InternalAttr("caller_cell", p.CallerCellID)))
	}
	if !certSet.Contains(expected) {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindMismatch,
			errcode.WithInternal(
				errcode.InternalAttr("cert_set", certSet.String()),
				errcode.InternalAttr("expected_id", expected.String()),
			))
	}
	return nil
}

// denyCrossBind logs (sanitized, structured) and writes the fail-closed error.
func denyCrossBind(w http.ResponseWriter, r *http.Request, err error) {
	attrs := []any{
		slog.String("method", logutil.Sanitize(r.Method)),
		slog.String("path", logutil.Sanitize(r.URL.Path)),
	}
	if rid, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
		attrs = append(attrs, slog.String("request_id", rid))
	}
	// r.Method / r.URL.Path are gosec-tainted net/http inputs routed through
	// logutil.Sanitize and emitted as structured slog attrs (slog quotes values
	// → no log-injection sink); see middleware.MTLS for the same pattern.
	slog.Warn("cross-cell identity binding rejected", attrs...) //nolint:gosec // G706: sanitized + structured slog
	httputil.WriteError(r.Context(), w, err)
}
