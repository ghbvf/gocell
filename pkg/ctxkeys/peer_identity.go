package ctxkeys

import (
	"context"
	"crypto/x509/pkix"
	"net/url"
)

const peerIdentity ctxKey = "peer_identity"

// PeerIdentity is the curated subset of a verified leaf X.509 client
// certificate surfaced to request-scope handlers.
//
// The field set is intentionally narrow: callers consume Subject (CN / O /
// OU / ...), DNS SANs, and URI SANs (including raw SPIFFE spiffe:// entries
// that callers parse with their own helper). Raw *x509.Certificate is
// deliberately not exposed, so handlers do not depend on the x509 internal
// representation. Adding a field requires a focused review of this file.
type PeerIdentity struct {
	Subject  pkix.Name
	DNSNames []string
	URIs     []*url.URL
}

// WithPeerIdentity returns a new context carrying the given peer identity.
// runtime/http/middleware.MTLS is the sole producer.
func WithPeerIdentity(ctx context.Context, id PeerIdentity) context.Context {
	return context.WithValue(ctx, peerIdentity, id)
}

// PeerIdentityFrom extracts the peer identity from ctx. The boolean reports
// whether the key was present; absent → zero-value PeerIdentity{} + false.
func PeerIdentityFrom(ctx context.Context) (PeerIdentity, bool) {
	v, ok := ctx.Value(peerIdentity).(PeerIdentity)
	return v, ok
}
