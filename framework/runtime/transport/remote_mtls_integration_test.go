//go:build integration

package transport_test

// remote_mtls_integration_test.go — end-to-end mTLS integration tests for the
// remote transport (#2263). A real TLS handshake runs over 127.0.0.1 between a
// client built from tlsutil.NewClientMTLSConfig and an httptest server using
// tlsutil.NewServerMTLSConfig, behind the full guard chain (middleware.MTLS →
// ServiceTokenMiddleware → PeerCellCrossBindMiddleware). Run with -tags=integration.
//
// These complement the unit tests (tlsutil VerifyConnection via constructed
// ConnectionState; celltransport gate logic): they prove the client and server
// TLS configs actually interoperate, that SPIFFE-ID peer authorization fires on a
// real handshake, and that the cross-bind rejects a cert↔caller-cell mismatch on
// a real request.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/http/middleware"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

const mtlsTrustDomain = "example.org"

// mtlsCA holds a self-signed CA and the pool that verifies certs it signs.
type mtlsCA struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	pemPEM []byte
	pool   *x509.CertPool
}

func newMTLSCA(t *testing.T) mtlsCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mtls-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return mtlsCA{cert: cert, key: key, pemPEM: caPEM, pool: pool}
}

// issueCellLeaf signs a leaf cert for cell (URI SAN spiffe://example.org/cell/<cell>,
// both ServerAuth + ClientAuth EKUs) and returns its PEM cert/key.
func (ca mtlsCA) issueCellLeaf(t *testing.T, cell string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	uri, err := url.Parse("spiffe://" + mtlsTrustDomain + "/cell/" + cell)
	if err != nil {
		t.Fatalf("spiffe uri: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cell},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// startMTLSServer starts an httptest TLS server for cell "configcore" behind the
// full guard chain: middleware.MTLS (peer identity) → ServiceTokenMiddleware
// (caller principal) → PeerCellCrossBindMiddleware (cert↔caller bind) → biz 200.
// RequireCallerCell is intentionally omitted so the cross-bind is the sole
// caller-cell check under test.
func startMTLSServer(t *testing.T, ca mtlsCA, ring *auth.HMACKeyRing, ns auth.NonceStore) *httptest.Server {
	t.Helper()
	serverCertPEM, serverKeyPEM := ca.issueCellLeaf(t, "configcore")
	serverCfg, err := tlsutil.NewServerMTLSConfig(serverCertPEM, serverKeyPEM, ca.pool)
	if err != nil {
		t.Fatalf("NewServerMTLSConfig: %v", err)
	}

	biz := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	})
	guarded := middleware.MTLS()(
		auth.ServiceTokenMiddleware(ring, clock.Real(), auth.WithServiceTokenNonceStore(ns))(
			auth.PeerCellCrossBindMiddleware()(biz),
		),
	)

	srv := httptest.NewUnstartedServer(guarded)
	srv.TLS = serverCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// mtlsClientTransport builds a RemoteHTTPTransport whose http.Client presents the
// given client cell cert and authorizes the server as expectedPeerCell (SPIFFE).
func mtlsClientTransport(t *testing.T, ca mtlsCA, clientCell, expectedPeerCell, serverURL string) *transport.RemoteHTTPTransport {
	t.Helper()
	clientCertPEM, clientKeyPEM := ca.issueCellLeaf(t, clientCell)
	expected, err := spiffeid.ForCell(mtlsTrustDomain, expectedPeerCell)
	if err != nil {
		t.Fatalf("ForCell: %v", err)
	}
	clientCfg, err := tlsutil.NewClientMTLSConfig(clientCertPEM, clientKeyPEM, ca.pool, expected)
	if err != nil {
		t.Fatalf("NewClientMTLSConfig: %v", err)
	}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: clientCfg}}
	resolver := transport.NewStaticResolver(map[string]string{"configcore": serverURL})
	return transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, httpClient, nil, nil)
}

// TestRemoteMTLS_Happy: client accesscore ↔ server configcore, token caller
// accesscore. Full handshake + SPIFFE verify + cross-bind all pass → 200.
func TestRemoteMTLS_Happy(t *testing.T) {
	t.Parallel()
	ca := newMTLSCA(t)
	ring := mustRing(t)
	ns := mustNonceStore(t)
	tid := mustTenantID(t)
	srv := startMTLSServer(t, ca, ring, ns)

	tr := mtlsClientTransport(t, ca, "accesscore", "configcore", srv.URL)
	req := signedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x", ring, tid, clock.Real())

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (happy mTLS path)", resp.StatusCode)
	}
}

// TestRemoteMTLS_ServerSPIFFEMismatch: the client authorizes the peer as
// "auditcore" but the server presents the "configcore" cert → the client's
// VerifyConnection rejects the handshake → DoContract returns a transport error.
func TestRemoteMTLS_ServerSPIFFEMismatch(t *testing.T) {
	t.Parallel()
	ca := newMTLSCA(t)
	ring := mustRing(t)
	ns := mustNonceStore(t)
	tid := mustTenantID(t)
	srv := startMTLSServer(t, ca, ring, ns) // server is configcore

	// Client expects the peer to be auditcore — mismatch with the configcore cert.
	tr := mtlsClientTransport(t, ca, "accesscore", "auditcore", srv.URL)
	req := signedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x", ring, tid, clock.Real())

	_, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err == nil {
		t.Fatal("expected handshake failure when server SPIFFE id != expected peer, got nil")
	}
}

// TestRemoteMTLS_CrossBindMismatch: the client presents the accesscore cert
// (handshake + server SPIFFE verify pass) but signs the service token as a
// DIFFERENT caller cell (configcore). The shared HMAC ring makes the token MAC
// valid, but the cross-bind rejects the cert(accesscore)↔caller(configcore)
// mismatch → 403.
func TestRemoteMTLS_CrossBindMismatch(t *testing.T) {
	t.Parallel()
	ca := newMTLSCA(t)
	ring := mustRing(t)
	ns := mustNonceStore(t)
	tid := mustTenantID(t)
	srv := startMTLSServer(t, ca, ring, ns)

	tr := mtlsClientTransport(t, ca, "accesscore", "configcore", srv.URL)

	// Cert is accesscore, but sign the token claiming caller=configcore.
	req := unsignedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x")
	if err := auth.SignInternalRequest(context.Background(), ring, "configcore", req, tid, clock.Real()); err != nil {
		t.Fatalf("SignInternalRequest: %v", err)
	}

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v (want nil error + 403 cross-bind response)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (cross-bind cert↔caller mismatch)", resp.StatusCode)
	}
}
