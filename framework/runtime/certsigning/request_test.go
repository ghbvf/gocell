package certsigning_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

const (
	testTenant  = "11111111-1111-1111-1111-111111111111"
	testTenantB = "22222222-2222-2222-2222-222222222222"
)

// testKey is a shared signing key for fabricating CSR / certificate DER in tests.
func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// testCSRDER fabricates a valid PKCS#10 CSR DER (properly self-signed, so
// CheckSignature passes). The subject is policy-decided from the request, not
// the CSR, so the CSR common name is fixed.
func testCSRDER(t *testing.T) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "device-1"}}, testKey(t))
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	return der
}

// testCertDER fabricates a self-signed certificate DER with the given serial.
func testCertDER(t *testing.T, serial *big.Int, notAfter time.Time) []byte {
	t.Helper()
	key := testKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "device"},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     notAfter,
	}
	// x509.CreateCertificate here is intentional and sanctioned by
	// TLS-TEST-MATERIAL-FUNNEL-01: this is the cert-signing pipeline under test,
	// so the cert is system-under-test input, not a reusable cell-identity
	// fixture — do NOT migrate to tlsutiltest (that would be circular).
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return der
}

// mustTenant parses a canonical tenant UUID for tests.
func mustTenant(t *testing.T, s string) tenant.TenantID {
	t.Helper()
	tid, err := tenant.ParseTenantID(s)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	return tid
}

// mustScope builds a valid CertScope for tests.
func mustScope(t *testing.T) cs.CertScope {
	t.Helper()
	iss, err := cs.NewIssuerID("ca-root")
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	dev, err := cs.NewDeviceID("device-1")
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	scope, err := cs.NewCertScope(mustTenant(t, testTenant), iss, dev)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return scope
}

// assertCode fails unless err carries the expected errcode.Code.
func assertCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var e *errcode.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *errcode.Error", err)
	}
	if e.Code != want {
		t.Fatalf("error code = %s, want %s", e.Code, want)
	}
}

// makeHex returns n hex digits, for over-length boundary tests.
func makeHex(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return b
}

func TestNewIssuerID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "valid", in: "ca-root"},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace", in: "   ", wantErr: true},
		{name: "too long", in: string(make([]byte, 300)), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := cs.NewIssuerID(tc.in)
			if tc.wantErr {
				assertCode(t, err, errcode.ErrCertScopeInvalid)
				if !id.IsZero() {
					t.Error("expected zero IssuerID on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id.String() != tc.in || id.IsZero() {
				t.Errorf("IssuerID round-trip: got %q", id.String())
			}
		})
	}
}

func TestNewDeviceID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "valid", in: "dev-1"},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace", in: "   ", wantErr: true},
		{name: "too long", in: string(make([]byte, 300)), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := cs.NewDeviceID(tc.in)
			if tc.wantErr {
				assertCode(t, err, errcode.ErrCertScopeInvalid)
				if !d.IsZero() {
					t.Error("expected zero DeviceID on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.String() != tc.in || d.IsZero() {
				t.Errorf("DeviceID round-trip: got %q", d.String())
			}
		})
	}
}

func TestNewSerial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "lowercase hex", in: "1a2b3c", want: "1a2b3c"},
		{name: "uppercase normalized", in: "ABCDEF", want: "abcdef"},
		{name: "empty", in: "", wantErr: true},
		{name: "non-hex", in: "xyz", wantErr: true},
		{name: "too long", in: string(makeHex(200)), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := cs.NewSerial(tc.in)
			if tc.wantErr {
				assertCode(t, err, errcode.ErrCertScopeInvalid)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.String() != tc.want {
				t.Errorf("serial = %q, want %q", s.String(), tc.want)
			}
		})
	}
	if !(cs.Serial{}).IsZero() {
		t.Error("zero Serial must report IsZero")
	}
}

func TestNewCertScope(t *testing.T) {
	t.Parallel()
	iss, _ := cs.NewIssuerID("ca-root")
	dev, _ := cs.NewDeviceID("dev-1")

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		scope, err := cs.NewCertScope(mustTenant(t, testTenant), iss, dev)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scope.IsZero() || scope.Issuer() != iss || scope.Device() != dev {
			t.Error("scope accessors mismatch")
		}
		if scope.Tenant().String() != testTenant {
			t.Errorf("scope tenant = %q", scope.Tenant().String())
		}
	})

	t.Run("invalid tenant", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewCertScope(tenant.TenantID("not-a-uuid"), iss, dev)
		assertCode(t, err, errcode.ErrCertScopeInvalid)
	})

	t.Run("zero issuer", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewCertScope(mustTenant(t, testTenant), cs.IssuerID{}, dev)
		assertCode(t, err, errcode.ErrCertScopeInvalid)
	})

	t.Run("zero device", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewCertScope(mustTenant(t, testTenant), iss, cs.DeviceID{})
		assertCode(t, err, errcode.ErrCertScopeInvalid)
	})
}

func TestCertScopeEqual(t *testing.T) {
	t.Parallel()
	iss, _ := cs.NewIssuerID("ca-root")
	issB, _ := cs.NewIssuerID("ca-other")
	dev, _ := cs.NewDeviceID("dev-1")

	a, _ := cs.NewCertScope(mustTenant(t, testTenant), iss, dev)
	same, _ := cs.NewCertScope(mustTenant(t, testTenant), iss, dev)
	otherTenant, _ := cs.NewCertScope(mustTenant(t, testTenantB), iss, dev)
	otherIssuer, _ := cs.NewCertScope(mustTenant(t, testTenant), issB, dev)

	if !a.Equal(same) {
		t.Error("identical scopes must be Equal")
	}
	if a.Equal(otherTenant) {
		t.Error("cross-tenant scopes must NOT be Equal")
	}
	if a.Equal(otherIssuer) {
		t.Error("cross-issuer scopes must NOT be Equal")
	}
}

func TestNewSubjectAltNames(t *testing.T) {
	t.Parallel()
	t.Run("blank dns rejected", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewSubjectAltNames([]string{" "}, nil, nil)
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("empty ip rejected", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewSubjectAltNames(nil, []net.IP{{}}, nil)
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("nil uri rejected", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewSubjectAltNames(nil, nil, []*url.URL{nil})
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("empty ok", func(t *testing.T) {
		t.Parallel()
		sans, err := cs.NewSubjectAltNames(nil, nil, nil)
		if err != nil || !sans.IsEmpty() {
			t.Fatalf("empty SANs: %v isEmpty=%v", err, sans.IsEmpty())
		}
	})
	t.Run("deep-copies inputs", func(t *testing.T) {
		t.Parallel()
		dns := []string{"a.example"}
		ips := []net.IP{net.ParseIP("10.0.0.1")}
		uris := []*url.URL{{Scheme: "spiffe", Host: "x"}}
		sans, err := cs.NewSubjectAltNames(dns, ips, uris)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		// Mutating the caller's inputs must not change the sealed value.
		dns[0] = "mutated"
		ips[0][0] = 0xff
		uris[0].Host = "mutated"
		if got := sans.DNSNames(); got[0] != "a.example" {
			t.Errorf("DNS not defensively copied: %q", got[0])
		}
		if got := sans.IPAddresses(); !got[0].Equal(net.ParseIP("10.0.0.1")) {
			t.Errorf("IP not deep-copied: %v", got[0])
		}
		if got := sans.URIs(); got[0].Host != "x" {
			t.Errorf("URI not deep-copied: %q", got[0].Host)
		}
		// Accessors return copies — mutating the result must not change internals.
		sans.DNSNames()[0] = "x"
		sans.IPAddresses()[0][0] = 0xff
		sans.URIs()[0].Host = "y"
		if sans.DNSNames()[0] != "a.example" || !sans.IPAddresses()[0].Equal(net.ParseIP("10.0.0.1")) || sans.URIs()[0].Host != "x" {
			t.Error("accessors must return deep copies")
		}
	})
}

func TestNewKeyUsages(t *testing.T) {
	t.Parallel()
	if _, err := cs.NewKeyUsages(0); err == nil {
		t.Error("zero key usage must error")
	}
	ku, err := cs.NewKeyUsages(x509.KeyUsageDigitalSignature, x509.ExtKeyUsageClientAuth)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ku.KeyUsage() != x509.KeyUsageDigitalSignature {
		t.Error("KeyUsage accessor mismatch")
	}
	if len(ku.ExtKeyUsage()) != 1 || ku.ExtKeyUsage()[0] != x509.ExtKeyUsageClientAuth {
		t.Error("ExtKeyUsage accessor mismatch")
	}
}

func TestNewDeviceSubject(t *testing.T) {
	t.Parallel()
	dev, _ := cs.NewDeviceID("dev-1")
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		sub, err := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "device-1.example")
		if err != nil || sub.IsZero() {
			t.Fatalf("valid subject: %v", err)
		}
		if sub.CommonName() != "device-1.example" || sub.Device() != dev {
			t.Error("subject accessors mismatch")
		}
		if sub.Tenant().String() != testTenant {
			t.Errorf("subject tenant = %q", sub.Tenant().String())
		}
	})
	t.Run("over-long cn", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, string(make([]byte, 300)))
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("whitespace cn", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "   ")
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("blank cn", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "")
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("invalid tenant", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewDeviceSubject(tenant.TenantID("bad"), dev, "cn")
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("zero device", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewDeviceSubject(mustTenant(t, testTenant), cs.DeviceID{}, "cn")
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
}

func TestNewCertRequest(t *testing.T) {
	t.Parallel()
	scope := mustScope(t)
	dev, _ := cs.NewDeviceID("device-1")
	subject, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "device-1")
	usages, _ := cs.NewKeyUsages(x509.KeyUsageDigitalSignature)
	sans, _ := cs.NewSubjectAltNames([]string{"device-1.example"}, nil, nil)
	csr := testCSRDER(t)

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		req, err := cs.NewCertRequest(scope, subject, csr, sans, usages, time.Hour)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if req.TTL() != time.Hour || req.Scope() != scope {
			t.Error("request accessors mismatch")
		}
		if req.Subject() != subject {
			t.Error("Subject accessor mismatch")
		}
		if req.SubjectAltNames().IsEmpty() {
			t.Error("SubjectAltNames accessor must carry the SAN")
		}
		if req.KeyUsages().KeyUsage() != x509.KeyUsageDigitalSignature {
			t.Error("KeyUsages accessor mismatch")
		}
		if len(req.CSRDER()) != len(csr) {
			t.Error("CSRDER mismatch")
		}
		// defensive copy on accessor
		req.CSRDER()[0] ^= 0xff
		if req.CSRDER()[0] == (csr[0] ^ 0xff) {
			t.Error("CSRDER must return a copy")
		}
	})

	tests := []struct {
		name    string
		scope   cs.CertScope
		subject cs.DeviceSubject
		csr     []byte
		ttl     time.Duration
	}{
		{name: "zero scope", scope: cs.CertScope{}, subject: subject, csr: csr, ttl: time.Hour},
		{name: "zero subject", scope: scope, subject: cs.DeviceSubject{}, csr: csr, ttl: time.Hour},
		{name: "empty csr", scope: scope, subject: subject, csr: nil, ttl: time.Hour},
		{name: "garbage csr", scope: scope, subject: subject, csr: []byte("not-a-csr"), ttl: time.Hour},
		{name: "non-positive ttl", scope: scope, subject: subject, csr: csr, ttl: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := cs.NewCertRequest(tc.scope, tc.subject, tc.csr, sans, usages, tc.ttl)
			assertCode(t, err, errcode.ErrCertRequestInvalid)
		})
	}

	t.Run("zero key usages", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewCertRequest(scope, subject, csr, sans, cs.KeyUsages{}, time.Hour)
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})

	t.Run("cross-device subject rejected", func(t *testing.T) {
		t.Parallel()
		devB, _ := cs.NewDeviceID("device-2")
		subjB, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), devB, "device-2")
		_, err := cs.NewCertRequest(scope, subjB, csr, sans, usages, time.Hour) // scope device-1 vs subject device-2
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})

	t.Run("cross-tenant subject rejected", func(t *testing.T) {
		t.Parallel()
		subjB, _ := cs.NewDeviceSubject(mustTenant(t, testTenantB), dev, "device-1")
		_, err := cs.NewCertRequest(scope, subjB, csr, sans, usages, time.Hour)
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})

	t.Run("csr proof-of-possession rejected (bad signature)", func(t *testing.T) {
		t.Parallel()
		bad := append([]byte(nil), csr...)
		bad[len(bad)-1] ^= 0xff // corrupt the signature; structure stays parseable
		_, err := cs.NewCertRequest(scope, subject, bad, sans, usages, time.Hour)
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
}

func TestNewIssuedCert(t *testing.T) {
	t.Parallel()
	scope := mustScope(t)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	certDER := testCertDER(t, big.NewInt(0x1a2b3c), notAfter)

	t.Run("valid derives serial and validity from DER", func(t *testing.T) {
		t.Parallel()
		chain := [][]byte{testCertDER(t, big.NewInt(0x01), notAfter)}
		ic, err := cs.NewIssuedCert(scope, certDER, chain, 3)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if ic.Serial().String() != "1a2b3c" {
			t.Errorf("derived serial = %q, want 1a2b3c", ic.Serial().String())
		}
		if !ic.NotAfter().Equal(notAfter) {
			t.Errorf("notAfter = %v", ic.NotAfter())
		}
		if !ic.NotBefore().Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("notBefore = %v", ic.NotBefore())
		}
		if ic.Epoch() != 3 {
			t.Errorf("epoch = %d", ic.Epoch())
		}
		if len(ic.DER()) != len(certDER) || len(ic.Chain()) != 1 {
			t.Error("DER/Chain accessor mismatch")
		}
		cert, err := ic.Certificate()
		if err != nil || cert.SerialNumber.Cmp(big.NewInt(0x1a2b3c)) != 0 {
			t.Errorf("Certificate() parse: %v", err)
		}
		// defensive copy
		ic.DER()[0] ^= 0xff
		if ic.DER()[0] == (certDER[0] ^ 0xff) {
			t.Error("DER must return a copy")
		}
	})

	t.Run("zero scope", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewIssuedCert(cs.CertScope{}, certDER, nil, 0)
		assertCode(t, err, errcode.ErrCertIssuedInvalid)
	})
	t.Run("empty der", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewIssuedCert(scope, nil, nil, 0)
		assertCode(t, err, errcode.ErrCertIssuedInvalid)
	})
	t.Run("garbage der", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewIssuedCert(scope, []byte("not-a-cert"), nil, 0)
		assertCode(t, err, errcode.ErrCertIssuedInvalid)
	})
}
