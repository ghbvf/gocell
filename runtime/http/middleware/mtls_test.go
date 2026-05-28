package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/http/tlsutil"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestMTLS_NoTLSStateReturns401(t *testing.T) {
	t.Parallel()
	handler := MTLS()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// req.TLS is nil by default
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMTLS_EmptyPeerChainReturns401(t *testing.T) {
	t.Parallel()
	handler := MTLS()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMTLS_401EnvelopeShape(t *testing.T) {
	t.Parallel()
	handler := MTLS()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", errObj["code"])
	assert.Equal(t, "mTLS client certificate required", errObj["message"])
	assert.Equal(t, []any{}, errObj["details"])
}

func TestMTLS_PeerIdentityFieldsInjected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cert *x509.Certificate
		want ctxkeys.PeerIdentity
	}{
		{
			name: "cn_only",
			cert: &x509.Certificate{
				Subject: pkix.Name{CommonName: "device-01"},
			},
			want: ctxkeys.PeerIdentity{
				Subject: pkix.Name{CommonName: "device-01"},
			},
		},
		{
			name: "subject_with_org_ou",
			cert: &x509.Certificate{
				Subject: pkix.Name{
					CommonName:         "device-02",
					Organization:       []string{"acme"},
					OrganizationalUnit: []string{"edge"},
				},
			},
			want: ctxkeys.PeerIdentity{
				Subject: pkix.Name{
					CommonName:         "device-02",
					Organization:       []string{"acme"},
					OrganizationalUnit: []string{"edge"},
				},
			},
		},
		{
			name: "dns_san",
			cert: &x509.Certificate{
				Subject:  pkix.Name{CommonName: "host-1"},
				DNSNames: []string{"host-1.example.com", "alt.example.com"},
			},
			want: ctxkeys.PeerIdentity{
				Subject:  pkix.Name{CommonName: "host-1"},
				DNSNames: []string{"host-1.example.com", "alt.example.com"},
			},
		},
		{
			name: "uri_san_with_spiffe",
			cert: &x509.Certificate{
				Subject: pkix.Name{CommonName: "wl-1"},
				URIs: []*url.URL{
					mustURL(t, "spiffe://example.org/ns/edge/sa/wl-1"),
					mustURL(t, "urn:device:1"),
				},
			},
			want: ctxkeys.PeerIdentity{
				Subject: pkix.Name{CommonName: "wl-1"},
				URIs: []*url.URL{
					mustURL(t, "spiffe://example.org/ns/edge/sa/wl-1"),
					mustURL(t, "urn:device:1"),
				},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got ctxkeys.PeerIdentity
			var found bool
			handler := MTLS()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, found = ctxkeys.PeerIdentityFrom(r.Context())
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.cert}}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			require.True(t, found)
			assert.Equal(t, tc.want.Subject.CommonName, got.Subject.CommonName)
			assert.Equal(t, tc.want.Subject.Organization, got.Subject.Organization)
			assert.Equal(t, tc.want.Subject.OrganizationalUnit, got.Subject.OrganizationalUnit)
			assert.Equal(t, tc.want.DNSNames, got.DNSNames)
			assert.Equal(t, tc.want.URIs, got.URIs)
		})
	}
}

// ─── Integration: end-to-end handshake via httptest.NewUnstartedServer ───────

// integTestChain holds server + client materials for a full mTLS round-trip.
type integTestChain struct {
	rootCertPEM   []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCert    tls.Certificate
	clientLeaf    *x509.Certificate
}

func genIntegChain(t *testing.T) integTestChain {
	t.Helper()

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "integ-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	rootCert, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)
	rootCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})

	// Server leaf — must include 127.0.0.1 / localhost in SAN for httptest.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "integ-server"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, rootCert, &serverKey.PublicKey, rootKey)
	require.NoError(t, err)
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	require.NoError(t, err)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	// Client leaf — CN + DNS SAN + URI SAN so the test asserts a non-trivial PeerIdentity.
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	spiffeURI, err := url.Parse("spiffe://example.org/ns/edge/sa/integ-client")
	require.NoError(t, err)
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject: pkix.Name{
			CommonName:   "integ-client",
			Organization: []string{"acme"},
		},
		DNSNames:    []string{"integ-client.example.com"},
		URIs:        []*url.URL{spiffeURI},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, rootCert, &clientKey.PublicKey, rootKey)
	require.NoError(t, err)
	clientLeaf, err := x509.ParseCertificate(clientDER)
	require.NoError(t, err)

	return integTestChain{
		rootCertPEM:   rootCertPEM,
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		clientCert: tls.Certificate{
			Certificate: [][]byte{clientDER},
			PrivateKey:  clientKey,
			Leaf:        clientLeaf,
		},
		clientLeaf: clientLeaf,
	}
}

func TestMTLS_IntegrationRoundTripViaHTTPTestServer(t *testing.T) {
	chain := genIntegChain(t)

	caPool, err := tlsutil.NewClientCAPool(chain.rootCertPEM)
	require.NoError(t, err)
	serverCfg, err := tlsutil.NewServerMTLSConfig(chain.serverCertPEM, chain.serverKeyPEM, caPool)
	require.NoError(t, err)

	var seenIdentity ctxkeys.PeerIdentity
	var seenOK bool
	handler := MTLS()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenIdentity, seenOK = ctxkeys.PeerIdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = serverCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)

	clientCAs := x509.NewCertPool()
	require.True(t, clientCAs.AppendCertsFromPEM(chain.rootCertPEM))
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      clientCAs,
				Certificates: []tls.Certificate{chain.clientCert},
				MinVersion:   tls.VersionTLS13,
			},
		},
	}

	resp, err := client.Get(srv.URL + "/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, seenOK)
	assert.Equal(t, "integ-client", seenIdentity.Subject.CommonName)
	assert.Equal(t, []string{"acme"}, seenIdentity.Subject.Organization)
	assert.Equal(t, []string{"integ-client.example.com"}, seenIdentity.DNSNames)
	require.Len(t, seenIdentity.URIs, 1)
	assert.Equal(t, "spiffe://example.org/ns/edge/sa/integ-client", seenIdentity.URIs[0].String())
}
