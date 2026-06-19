package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
)

// expiredLeafOffset is the offset used to produce an already-expired leaf cert
// (TEST-TIME-LITERAL-01: site-specific test deadline as a file-local const, not an inline literal).
const expiredLeafOffset = -time.Minute

func bothAuth() []x509.ExtKeyUsage {
	return []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
}

func mustCellID(t *testing.T, td, cell string) spiffeid.CellID {
	t.Helper()
	id, err := spiffeid.ForCell(td, cell)
	require.NoError(t, err)
	return id
}

func TestNewClientMTLSConfig_PinsFailClosedDefaults(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/configcore")},
		EKU:  bothAuth(),
	})

	cfg, err := NewClientMTLSConfig(leaf.CertPEM, leaf.KeyPEM, ca.Pool, mustCellID(t, "example.org", "configcore"))
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion, "MinVersion must be pinned to TLS 1.3")
	assert.True(t, cfg.InsecureSkipVerify, "InsecureSkipVerify must be true (hostname check replaced by SPIFFE-ID verify)")
	assert.NotNil(t, cfg.VerifyConnection, "VerifyConnection must be set — it is the actual peer auth")
	assert.Len(t, cfg.Certificates, 1, "client certificate must be presented for mutual TLS")
}

func TestNewClientMTLSConfig_ErrorPaths(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/configcore")},
		EKU:  bothAuth(),
	})
	id := mustCellID(t, "example.org", "configcore")

	// A mismatched key: issue a second leaf from the same CA so we have a valid PEM
	// key that doesn't correspond to leaf.CertPEM.
	otherLeaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{EKU: bothAuth()})

	tests := []struct {
		name    string
		cert    []byte
		key     []byte
		pool    *x509.CertPool
		peer    spiffeid.CellID
		wantErr bool
	}{
		{name: "ok", cert: leaf.CertPEM, key: leaf.KeyPEM, pool: ca.Pool, peer: id, wantErr: false},
		{name: "empty cert", cert: nil, key: leaf.KeyPEM, pool: ca.Pool, peer: id, wantErr: true},
		{name: "empty key", cert: leaf.CertPEM, key: nil, pool: ca.Pool, peer: id, wantErr: true},
		{name: "nil rootCAs", cert: leaf.CertPEM, key: leaf.KeyPEM, pool: nil, peer: id, wantErr: true},
		{name: "zero expected peer id", cert: leaf.CertPEM, key: leaf.KeyPEM, pool: ca.Pool, peer: spiffeid.CellID{}, wantErr: true},
		{
			name: "mismatched cert/key", cert: leaf.CertPEM,
			key:  otherLeaf.KeyPEM,
			pool: ca.Pool, peer: id, wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClientMTLSConfig(tc.cert, tc.key, tc.pool, tc.peer)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestVerifyConnection exercises the actual peer-auth callback against a
// constructed ConnectionState (no full handshake — that is the batch-7
// integration test). This is where fail-open regressions would surface.
func TestVerifyConnection(t *testing.T) {
	t.Parallel()
	expected := mustCellID(t, "example.org", "configcore")

	// Build the config once against a server peer whose cert chains to `serverCA`
	// and asserts identity == expected.
	serverCA := tlsutiltest.NewCA(t)
	server := serverCA.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/configcore")},
		EKU:  bothAuth(),
	})
	// A client cert/key just to satisfy the config builder; not used by verify.
	clientCA := tlsutiltest.NewCA(t)
	client := clientCA.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/accesscore")},
		EKU:  bothAuth(),
	})

	build := func(peerCAPool *x509.CertPool, peer spiffeid.CellID) *tls.Config {
		cfg, err := NewClientMTLSConfig(client.CertPEM, client.KeyPEM, peerCAPool, peer)
		require.NoError(t, err)
		return cfg
	}

	t.Run("valid: chained + matching SPIFFE id", func(t *testing.T) {
		t.Parallel()
		cfg := build(serverCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{server.Cert}})
		assert.NoError(t, err)
	})

	t.Run("reject: no peer certificate", func(t *testing.T) {
		t.Parallel()
		cfg := build(serverCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{})
		assert.Error(t, err)
	})

	t.Run("reject: wrong cell SPIFFE id", func(t *testing.T) {
		t.Parallel()
		wrongCA := tlsutiltest.NewCA(t)
		wrong := wrongCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/auditcore")},
			EKU:  bothAuth(),
		})
		cfg := build(wrongCA.Pool, expected) // expects configcore, peer is auditcore
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{wrong.Cert}})
		assert.Error(t, err)
	})

	t.Run("reject: wrong trust domain", func(t *testing.T) {
		t.Parallel()
		otherCA := tlsutiltest.NewCA(t)
		other := otherCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://other.org/cell/configcore")},
			EKU:  bothAuth(),
		})
		cfg := build(otherCA.Pool, expected) // expects example.org, peer is other.org
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{other.Cert}})
		assert.Error(t, err)
	})

	t.Run("reject: no SPIFFE SAN", func(t *testing.T) {
		t.Parallel()
		bareCA := tlsutiltest.NewCA(t)
		bare := bareCA.IssueLeaf(t, tlsutiltest.LeafOptions{EKU: bothAuth()})
		cfg := build(bareCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{bare.Cert}})
		assert.Error(t, err)
	})

	t.Run("reject: untrusted chain (different CA)", func(t *testing.T) {
		t.Parallel()
		// Verify against an unrelated CA pool: chain build must fail even though
		// the SPIFFE id matches — proves we are NOT fail-open on chain.
		unrelatedCA := tlsutiltest.NewCA(t)
		unrelated := unrelatedCA.IssueLeaf(t, tlsutiltest.LeafOptions{EKU: bothAuth()})
		_ = unrelated
		cfg := build(unrelatedCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{server.Cert}})
		assert.Error(t, err)
	})

	t.Run("reject: expired leaf", func(t *testing.T) {
		t.Parallel()
		expiredCA := tlsutiltest.NewCA(t)
		expired := expiredCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs:     []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/configcore")},
			EKU:      bothAuth(),
			NotAfter: time.Now().Add(expiredLeafOffset),
		})
		cfg := build(expiredCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{expired.Cert}})
		assert.Error(t, err)
	})

	t.Run("reject: peer lacks ServerAuth EKU", func(t *testing.T) {
		t.Parallel()
		clientOnlyCA := tlsutiltest.NewCA(t)
		clientOnly := clientOnlyCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/configcore")},
			EKU:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		cfg := build(clientOnlyCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{clientOnly.Cert}})
		assert.Error(t, err)
	})

	// F9.1: exercises the spiffeid.FromURIs err != nil branch in verifyPeerCellIdentity.
	// A leaf that presents TWO distinct cell SPIFFE URIs is ambiguous — VerifyConnection
	// must reject it regardless of whether either URI matches the expected peer.
	t.Run("reject: ambiguous cell SPIFFE id (two distinct cell URIs)", func(t *testing.T) {
		t.Parallel()
		ambiguousCA := tlsutiltest.NewCA(t)
		ambiguous := ambiguousCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs: []*url.URL{
				tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/accesscore"),
				tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/auditcore"),
			},
			EKU: bothAuth(),
		})
		// Use the outer `expected` (configcore) as the expected peer identity: the
		// ambiguity check fires before the identity comparison, so the expected peer
		// does not matter for the error path.
		cfg := build(ambiguousCA.Pool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{ambiguous.Cert}})
		assert.Error(t, err, "ambiguous SPIFFE id must be rejected")
	})
}

func TestClientIdentity(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/accesscore")},
		EKU:  bothAuth(),
	})

	t.Run("zero value IsZero", func(t *testing.T) {
		t.Parallel()
		var zero ClientIdentity
		assert.True(t, zero.IsZero())
		_, err := zero.ConfigForPeer("configcore")
		assert.Error(t, err, "ConfigForPeer on a zero ClientIdentity must fail-closed")
	})

	t.Run("constructed + ConfigForPeer binds expected id", func(t *testing.T) {
		t.Parallel()
		ci, err := NewClientIdentity(leaf.CertPEM, leaf.KeyPEM, ca.Pool, "example.org")
		require.NoError(t, err)
		assert.False(t, ci.IsZero())

		cfg, err := ci.ConfigForPeer("configcore")
		require.NoError(t, err)
		require.NotNil(t, cfg.VerifyConnection)

		// The minted config must accept a configcore peer and reject an auditcore peer.
		goodCA := tlsutiltest.NewCA(t)
		good := goodCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/configcore")},
			EKU:  bothAuth(),
		})
		// Re-issue the good peer under the SAME root as ci so the chain verifies.
		ciSameRoot, err := NewClientIdentity(leaf.CertPEM, leaf.KeyPEM, goodCA.Pool, "example.org")
		require.NoError(t, err)
		cfg2, err := ciSameRoot.ConfigForPeer("configcore")
		require.NoError(t, err)
		assert.NoError(t, cfg2.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{good.Cert}}))

		badCA := tlsutiltest.NewCA(t)
		bad := badCA.IssueLeaf(t, tlsutiltest.LeafOptions{
			URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/auditcore")},
			EKU:  bothAuth(),
		})
		ciBadRoot, err := NewClientIdentity(leaf.CertPEM, leaf.KeyPEM, badCA.Pool, "example.org")
		require.NoError(t, err)
		cfg3, err := ciBadRoot.ConfigForPeer("configcore")
		require.NoError(t, err)
		assert.Error(t, cfg3.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{bad.Cert}}))
	})

	t.Run("error paths", func(t *testing.T) {
		t.Parallel()
		_, err := NewClientIdentity(nil, leaf.KeyPEM, ca.Pool, "example.org")
		assert.Error(t, err, "empty cert")
		_, err = NewClientIdentity(leaf.CertPEM, leaf.KeyPEM, nil, "example.org")
		assert.Error(t, err, "nil rootCAs")
		_, err = NewClientIdentity(leaf.CertPEM, leaf.KeyPEM, ca.Pool, "")
		assert.Error(t, err, "empty trust domain")
		_, err = NewClientIdentity(leaf.CertPEM, leaf.KeyPEM, ca.Pool, "Example.ORG")
		assert.Error(t, err, "invalid (uppercase) trust domain")
	})
}
