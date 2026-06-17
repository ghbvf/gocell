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
	msgCrossBindNoPeer      = "mTLS peer certificate required for cross-cell identity binding"
	msgCrossBindNoCertCell  = "mTLS peer certificate carries no cell SPIFFE ID (spiffe://<td>/cell/<cell>)"
	msgCrossBindNoPrincipal = "service-token caller principal required for cross-cell identity binding"
	msgCrossBindMismatch    = "mTLS peer cell identity does not match the service-token caller cell"
)

// PeerCellCrossBindMiddleware binds the transport-layer mTLS identity to the
// message-layer service-token identity (#2263): it requires the client
// certificate's SPIFFE cell ID (URI SAN spiffe://<td>/cell/<cell>) to equal the
// authenticated service-token caller cell ([Principal.CallerCellID]). On any
// mismatch — or a missing peer cert / cell SPIFFE ID / service principal — it
// fails closed (403, except a missing peer cert which is 401).
//
// This is the core of "peer authentication": #2153 (per-cell ProvisionedKeyring)
// already authenticates WHICH cell sent the message at the token layer, and mTLS
// authenticates the connection endpoint at the transport layer; this guard ties
// the two into ONE coherent identity, so a cell presenting cell A's certificate
// cannot claim to be caller B (and vice versa) — and a misprovisioning (cert and
// key for different cells) fails closed instead of silently passing.
//
// Ordering: it MUST run AFTER both [middleware.MTLS] (which populates
// ctxkeys.PeerIdentity) and [ServiceTokenMiddleware] (which populates the
// Principal). Bootstrap installs it last on the internal listener whenever the
// auth chain contains BOTH AuthMTLS and AuthServiceToken (see
// applyListenerAuthChain), so the ordering holds by construction.
func PeerCellCrossBindMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, ok := ctxkeys.PeerIdentityFrom(r.Context())
			if !ok {
				denyCrossBind(w, r,
					errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgCrossBindNoPeer))
				return
			}
			certCell, ok, err := spiffeid.FromURIs(peer.URIs)
			if err != nil || !ok {
				denyCrossBind(w, r,
					errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindNoCertCell))
				return
			}
			p, ok := FromContext(r.Context())
			if !ok || p.CallerCellID == "" {
				denyCrossBind(w, r,
					errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindNoPrincipal))
				return
			}
			if certCell.Cell() != p.CallerCellID {
				denyCrossBind(w, r,
					errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgCrossBindMismatch,
						errcode.WithInternal(
							errcode.InternalAttr("cert_cell", certCell.Cell()),
							errcode.InternalAttr("caller_cell", p.CallerCellID),
						)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
