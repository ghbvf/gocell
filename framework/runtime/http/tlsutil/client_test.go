package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
)

// cellChain holds a self-signed root + a cell leaf carrying a SPIFFE URI SAN.
type cellChain struct {
	rootCertPEM []byte
	rootPool    *x509.CertPool
	leafCertPEM []byte
	leafKeyPEM  []byte
	leaf        *x509.Certificate
}

// genCellChain builds a root CA and a leaf whose URI SANs are `uris`, signed by
// the root, with the given ExtKeyUsages and validity window. ECDSA P-256.
func genCellChain(t *testing.T, uris []*url.URL, eku []x509.ExtKeyUsage, notAfter time.Time) cellChain {
	t.Helper()

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	rootCert, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-cell"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		URIs:         uris,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, rootCert, &leafKey.PublicKey, rootKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	require.NoError(t, err)

	rootPool := x509.NewCertPool()
	rootPool.AddCert(rootCert)

	return cellChain{
		rootCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		rootPool:    rootPool,
		leafCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		leafKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}),
		leaf:        leaf,
	}
}

func mustURI(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

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
	ch := genCellChain(t,
		[]*url.URL{mustURI(t, "spiffe://example.org/cell/configcore")},
		bothAuth(), time.Now().Add(time.Hour))

	cfg, err := NewClientMTLSConfig(ch.leafCertPEM, ch.leafKeyPEM, ch.rootPool, mustCellID(t, "example.org", "configcore"))
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion, "MinVersion must be pinned to TLS 1.3")
	assert.True(t, cfg.InsecureSkipVerify, "InsecureSkipVerify must be true (hostname check replaced by SPIFFE-ID verify)")
	assert.NotNil(t, cfg.VerifyConnection, "VerifyConnection must be set — it is the actual peer auth")
	assert.Len(t, cfg.Certificates, 1, "client certificate must be presented for mutual TLS")
}

func TestNewClientMTLSConfig_ErrorPaths(t *testing.T) {
	t.Parallel()
	ch := genCellChain(t,
		[]*url.URL{mustURI(t, "spiffe://example.org/cell/configcore")},
		bothAuth(), time.Now().Add(time.Hour))
	id := mustCellID(t, "example.org", "configcore")

	tests := []struct {
		name    string
		cert    []byte
		key     []byte
		pool    *x509.CertPool
		peer    spiffeid.CellID
		wantErr bool
	}{
		{name: "ok", cert: ch.leafCertPEM, key: ch.leafKeyPEM, pool: ch.rootPool, peer: id, wantErr: false},
		{name: "empty cert", cert: nil, key: ch.leafKeyPEM, pool: ch.rootPool, peer: id, wantErr: true},
		{name: "empty key", cert: ch.leafCertPEM, key: nil, pool: ch.rootPool, peer: id, wantErr: true},
		{name: "nil rootCAs", cert: ch.leafCertPEM, key: ch.leafKeyPEM, pool: nil, peer: id, wantErr: true},
		{name: "zero expected peer id", cert: ch.leafCertPEM, key: ch.leafKeyPEM, pool: ch.rootPool, peer: spiffeid.CellID{}, wantErr: true},
		{
			name: "mismatched cert/key", cert: ch.leafCertPEM,
			key:  genCellChain(t, nil, bothAuth(), time.Now().Add(time.Hour)).leafKeyPEM,
			pool: ch.rootPool, peer: id, wantErr: true,
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
	server := genCellChain(t,
		[]*url.URL{mustURI(t, "spiffe://example.org/cell/configcore")},
		bothAuth(), time.Now().Add(time.Hour))
	// A client cert/key just to satisfy the config builder; not used by verify.
	client := genCellChain(t,
		[]*url.URL{mustURI(t, "spiffe://example.org/cell/accesscore")},
		bothAuth(), time.Now().Add(time.Hour))

	build := func(peerCAPool *x509.CertPool, peer spiffeid.CellID) *tls.Config {
		cfg, err := NewClientMTLSConfig(client.leafCertPEM, client.leafKeyPEM, peerCAPool, peer)
		require.NoError(t, err)
		return cfg
	}

	t.Run("valid: chained + matching SPIFFE id", func(t *testing.T) {
		t.Parallel()
		cfg := build(server.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{server.leaf}})
		assert.NoError(t, err)
	})

	t.Run("reject: no peer certificate", func(t *testing.T) {
		t.Parallel()
		cfg := build(server.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{})
		assert.Error(t, err)
	})

	t.Run("reject: wrong cell SPIFFE id", func(t *testing.T) {
		t.Parallel()
		wrong := genCellChain(t,
			[]*url.URL{mustURI(t, "spiffe://example.org/cell/auditcore")},
			bothAuth(), time.Now().Add(time.Hour))
		cfg := build(wrong.rootPool, expected) // expects configcore, peer is auditcore
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{wrong.leaf}})
		assert.Error(t, err)
	})

	t.Run("reject: wrong trust domain", func(t *testing.T) {
		t.Parallel()
		other := genCellChain(t,
			[]*url.URL{mustURI(t, "spiffe://other.org/cell/configcore")},
			bothAuth(), time.Now().Add(time.Hour))
		cfg := build(other.rootPool, expected) // expects example.org, peer is other.org
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{other.leaf}})
		assert.Error(t, err)
	})

	t.Run("reject: no SPIFFE SAN", func(t *testing.T) {
		t.Parallel()
		bare := genCellChain(t, nil, bothAuth(), time.Now().Add(time.Hour))
		cfg := build(bare.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{bare.leaf}})
		assert.Error(t, err)
	})

	t.Run("reject: untrusted chain (different CA)", func(t *testing.T) {
		t.Parallel()
		// Verify against an unrelated CA pool: chain build must fail even though
		// the SPIFFE id matches — proves we are NOT fail-open on chain.
		unrelated := genCellChain(t, nil, bothAuth(), time.Now().Add(time.Hour))
		cfg := build(unrelated.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{server.leaf}})
		assert.Error(t, err)
	})

	t.Run("reject: expired leaf", func(t *testing.T) {
		t.Parallel()
		expired := genCellChain(t,
			[]*url.URL{mustURI(t, "spiffe://example.org/cell/configcore")},
			bothAuth(), time.Now().Add(-time.Minute))
		cfg := build(expired.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{expired.leaf}})
		assert.Error(t, err)
	})

	t.Run("reject: peer lacks ServerAuth EKU", func(t *testing.T) {
		t.Parallel()
		clientOnly := genCellChain(t,
			[]*url.URL{mustURI(t, "spiffe://example.org/cell/configcore")},
			[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, time.Now().Add(time.Hour))
		cfg := build(clientOnly.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{clientOnly.leaf}})
		assert.Error(t, err)
	})

	// F9.1: exercises the spiffeid.FromURIs err != nil branch in verifyPeerCellIdentity.
	// A leaf that presents TWO distinct cell SPIFFE URIs is ambiguous — VerifyConnection
	// must reject it regardless of whether either URI matches the expected peer.
	t.Run("reject: ambiguous cell SPIFFE id (two distinct cell URIs)", func(t *testing.T) {
		t.Parallel()
		ambiguous := genCellChain(t,
			[]*url.URL{
				mustURI(t, "spiffe://example.org/cell/accesscore"),
				mustURI(t, "spiffe://example.org/cell/auditcore"),
			},
			bothAuth(), time.Now().Add(time.Hour))
		// Use the outer `expected` (configcore) as the expected peer identity: the
		// ambiguity check fires before the identity comparison, so the expected peer
		// does not matter for the error path.
		cfg := build(ambiguous.rootPool, expected)
		err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{ambiguous.leaf}})
		assert.Error(t, err, "ambiguous SPIFFE id must be rejected")
	})
}

func TestClientIdentity(t *testing.T) {
	t.Parallel()
	ch := genCellChain(t,
		[]*url.URL{mustURI(t, "spiffe://example.org/cell/accesscore")},
		bothAuth(), time.Now().Add(time.Hour))

	t.Run("zero value IsZero", func(t *testing.T) {
		t.Parallel()
		var zero ClientIdentity
		assert.True(t, zero.IsZero())
		_, err := zero.ConfigForPeer("configcore")
		assert.Error(t, err, "ConfigForPeer on a zero ClientIdentity must fail-closed")
	})

	t.Run("constructed + ConfigForPeer binds expected id", func(t *testing.T) {
		t.Parallel()
		ci, err := NewClientIdentity(ch.leafCertPEM, ch.leafKeyPEM, ch.rootPool, "example.org")
		require.NoError(t, err)
		assert.False(t, ci.IsZero())

		cfg, err := ci.ConfigForPeer("configcore")
		require.NoError(t, err)
		require.NotNil(t, cfg.VerifyConnection)

		// The minted config must accept a configcore peer and reject an auditcore peer.
		good := genCellChain(t, []*url.URL{mustURI(t, "spiffe://example.org/cell/configcore")}, bothAuth(), time.Now().Add(time.Hour))
		// Re-issue the good peer under the SAME root as ci so the chain verifies.
		ciSameRoot, err := NewClientIdentity(ch.leafCertPEM, ch.leafKeyPEM, good.rootPool, "example.org")
		require.NoError(t, err)
		cfg2, err := ciSameRoot.ConfigForPeer("configcore")
		require.NoError(t, err)
		assert.NoError(t, cfg2.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{good.leaf}}))

		bad := genCellChain(t, []*url.URL{mustURI(t, "spiffe://example.org/cell/auditcore")}, bothAuth(), time.Now().Add(time.Hour))
		ciBadRoot, err := NewClientIdentity(ch.leafCertPEM, ch.leafKeyPEM, bad.rootPool, "example.org")
		require.NoError(t, err)
		cfg3, err := ciBadRoot.ConfigForPeer("configcore")
		require.NoError(t, err)
		assert.Error(t, cfg3.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{bad.leaf}}))
	})

	t.Run("error paths", func(t *testing.T) {
		t.Parallel()
		_, err := NewClientIdentity(nil, ch.leafKeyPEM, ch.rootPool, "example.org")
		assert.Error(t, err, "empty cert")
		_, err = NewClientIdentity(ch.leafCertPEM, ch.leafKeyPEM, nil, "example.org")
		assert.Error(t, err, "nil rootCAs")
		_, err = NewClientIdentity(ch.leafCertPEM, ch.leafKeyPEM, ch.rootPool, "")
		assert.Error(t, err, "empty trust domain")
		_, err = NewClientIdentity(ch.leafCertPEM, ch.leafKeyPEM, ch.rootPool, "Example.ORG")
		assert.Error(t, err, "invalid (uppercase) trust domain")
	})
}
