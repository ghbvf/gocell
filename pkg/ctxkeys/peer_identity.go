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
//
// SECURITY NOTE: Subject is the stdlib pkix.Name. Beyond CN/O/OU it carries
// Names []pkix.AttributeTypeAndValue and ExtraNames []pkix.AttributeTypeAndValue
// which can hold arbitrary OID-value pairs (e.g. emailAddress, serialNumber).
// Callers that serialize Subject wholesale into slog or access logs may leak
// PII. Read only the named fields you need (Subject.CommonName,
// Subject.Organization, etc.) and never marshal Subject directly into wire
// or log payloads.
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
//
// CALLERS MUST CHECK ok BEFORE USING THE IDENTITY. Absent means the request
// did not pass through runtime/http/middleware.MTLS (e.g. mTLS not on this
// listener, or the request hit a non-mTLS auth chain). Using the zero-value
// PeerIdentity{} as if it were a real identity is a silent bug — it has an
// empty CN, nil DNSNames, and nil URIs that would compare equal to other
// zero values.
func PeerIdentityFrom(ctx context.Context) (PeerIdentity, bool) {
	v, ok := ctx.Value(peerIdentity).(PeerIdentity)
	return v, ok
}
