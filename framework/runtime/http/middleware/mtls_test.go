package middleware

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
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

// TestMTLS_PeerIdentityIsolatedFromCert locks F1 (cluster C1): the PeerIdentity
// handed to handlers must own all of its slices/pointers, so a handler mutating
// it cannot corrupt the connection-cached *x509.Certificate (which net/http
// reuses across keep-alive requests). The handler below mutates every mutable
// field of the identity; afterwards the source cert must be byte-for-byte
// unchanged.
func TestMTLS_PeerIdentityIsolatedFromCert(t *testing.T) {
	t.Parallel()

	cert := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:         "wl-1",
			Organization:       []string{"acme"},
			OrganizationalUnit: []string{"edge"},
			Country:            []string{"US"},
			Locality:           []string{"sf"},
			Province:           []string{"ca"},
			StreetAddress:      []string{"1 main"},
			PostalCode:         []string{"94105"},
		},
		DNSNames: []string{"host-1.example.com", "alt.example.com"},
		URIs: []*url.URL{
			mustURL(t, "spiffe://example.org/ns/edge/sa/wl-1"),
		},
	}

	// Snapshot the cert's mutable state before the request.
	wantDNS := append([]string(nil), cert.DNSNames...)
	wantOrg := append([]string(nil), cert.Subject.Organization...)
	wantOU := append([]string(nil), cert.Subject.OrganizationalUnit...)
	wantCountry := append([]string(nil), cert.Subject.Country...)
	wantURIHost := cert.URIs[0].Host
	wantURIPath := cert.URIs[0].Path

	handler := MTLS()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ctxkeys.PeerIdentityFrom(r.Context())
		require.True(t, ok)
		// Hostile handler: mutate every slice/pointer field in place.
		id.DNSNames[0] = "evil.example.com"
		id.Subject.Organization[0] = "evil-org"
		id.Subject.OrganizationalUnit[0] = "evil-ou"
		id.Subject.Country[0] = "ZZ"
		id.URIs[0].Host = "evil.example.org"
		id.URIs[0].Path = "/evil"
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// The source certificate must be untouched by the handler's mutations.
	assert.Equal(t, wantDNS, cert.DNSNames, "cert.DNSNames must not alias the identity")
	assert.Equal(t, wantOrg, cert.Subject.Organization, "cert.Subject.Organization must not alias")
	assert.Equal(t, wantOU, cert.Subject.OrganizationalUnit, "cert.Subject.OrganizationalUnit must not alias")
	assert.Equal(t, wantCountry, cert.Subject.Country, "cert.Subject.Country must not alias")
	assert.Equal(t, wantURIHost, cert.URIs[0].Host, "cert.URIs[0] must not alias the identity URL")
	assert.Equal(t, wantURIPath, cert.URIs[0].Path, "cert.URIs[0] must not alias the identity URL")
}

// ─── Integration: end-to-end handshake via httptest.NewUnstartedServer ───────

func TestMTLS_IntegrationRoundTripViaHTTPTestServer(t *testing.T) {
	ca := tlsutiltest.NewCA(t)

	server := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		IPs:      []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	client := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs:     []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/ns/edge/sa/integ-client")},
		DNSNames: []string{"integ-client.example.com"},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	caPool, err := tlsutil.NewClientCAPool(ca.CertPEM)
	require.NoError(t, err)
	serverCfg, err := tlsutil.NewServerMTLSConfig(server.CertPEM, server.KeyPEM, caPool)
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
	require.True(t, clientCAs.AppendCertsFromPEM(ca.CertPEM))
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      clientCAs,
				Certificates: []tls.Certificate{client.TLSCert},
				MinVersion:   tls.VersionTLS13,
			},
		},
	}

	resp, err := httpClient.Get(srv.URL + "/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, seenOK)
	assert.Equal(t, "tlsutiltest-leaf", seenIdentity.Subject.CommonName)
	assert.Equal(t, []string{"integ-client.example.com"}, seenIdentity.DNSNames)
	require.Len(t, seenIdentity.URIs, 1)
	assert.Equal(t, "spiffe://example.org/ns/edge/sa/integ-client", seenIdentity.URIs[0].String())
}
