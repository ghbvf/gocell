package ctxkeys

import (
	"context"
	"crypto/x509/pkix"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestPeerIdentityRoundTrip(t *testing.T) {
	want := PeerIdentity{
		Subject: pkix.Name{
			CommonName:   "device-01",
			Organization: []string{"acme"},
		},
		DNSNames: []string{"device-01.example.com", "alt.example.com"},
		URIs: []*url.URL{
			mustParseURL(t, "spiffe://example.org/ns/edge/sa/device-01"),
			mustParseURL(t, "urn:device:01"),
		},
	}

	ctx := WithPeerIdentity(context.Background(), want)
	got, ok := PeerIdentityFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, want.Subject.CommonName, got.Subject.CommonName)
	assert.Equal(t, want.Subject.Organization, got.Subject.Organization)
	assert.Equal(t, want.DNSNames, got.DNSNames)
	assert.Equal(t, want.URIs, got.URIs)
}

func TestPeerIdentityFrom_MissingKey(t *testing.T) {
	got, ok := PeerIdentityFrom(context.Background())
	assert.False(t, ok)
	assert.Equal(t, PeerIdentity{}, got)
}

func TestPeerIdentity_CoexistsWithRequestScopeKeys(t *testing.T) {
	id := PeerIdentity{Subject: pkix.Name{CommonName: "device-02"}}
	ctx := context.Background()
	ctx = WithRequestID(ctx, "req-001")
	ctx = WithRealIP(ctx, "10.0.0.1")
	ctx = WithPeerIdentity(ctx, id)

	reqID, ok := RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-001", reqID)

	ip, ok := RealIPFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "10.0.0.1", ip)

	got, ok := PeerIdentityFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "device-02", got.Subject.CommonName)
}
