package tlsutiltest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Validity windows for generated test certs. TEST-TIME-LITERAL-01: site-specific
// test deadlines as file-local consts, not inline literals.
const (
	// certBackdate moves NotBefore into the past so a freshly generated cert is
	// already valid (no clock-skew flakiness).
	certBackdate = time.Hour
	// caValidity is the (generous) validity window of a test root CA — wide
	// enough that no test's leaf outlives it.
	caValidity = 24 * time.Hour
	// defaultLeafValidity is the leaf window used when LeafOptions.NotAfter is
	// the zero value.
	defaultLeafValidity = time.Hour
)

// File permissions for WriteFiles: the leaf private key is owner-only; the leaf
// cert and CA cert are public material (world-readable), matching the
// adapters/softca convention (key 0o600, cert 0o644).
const (
	keyFileMode  os.FileMode = 0o600
	certFileMode os.FileMode = 0o644
)

// CA is a self-signed ECDSA P-256 root CA for tests. It can issue any number of
// leaves via IssueLeaf; each gets a distinct, monotonically increasing serial
// (root = 1, leaves = 2,3,…). Construct it with NewCA. A *CA is safe for
// concurrent IssueLeaf calls from multiple goroutines — the serial counter is
// atomic and the rest of issuance (key-gen, signing) is local.
type CA struct {
	// Cert is the parsed root certificate.
	Cert *x509.Certificate
	// CertPEM is the PEM-encoded root certificate.
	CertPEM []byte
	// Pool is a *x509.CertPool containing only this root — ready to pass as the
	// trust root to NewClientCAPool consumers or tls verification.
	Pool *x509.CertPool

	key    *ecdsa.PrivateKey
	serial atomic.Int64 // last issued serial; root = 1, leaves start at 2
}

// NewCA generates a self-signed ECDSA P-256 root CA with a generous validity
// window.
func NewCA(t *testing.T) *CA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("tlsutiltest: generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tlsutiltest-root"},
		NotBefore:             time.Now().Add(-certBackdate),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("tlsutiltest: create CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("tlsutiltest: parse CA cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	ca := &CA{
		Cert:    cert,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Pool:    pool,
		key:     key,
	}
	ca.serial.Store(1) // root cert used serial 1; leaves start at 2
	return ca
}

// LeafOptions configures a leaf cert issued by IssueLeaf. The zero value yields
// a Server+Client-auth leaf with no SANs valid for defaultLeafValidity — set
// the fields the test needs.
type LeafOptions struct {
	// URIs are URI SANs, typically SPIFFE IDs (see SPIFFEURI).
	URIs []*url.URL
	// DNSNames are DNS SANs.
	DNSNames []string
	// IPs are IP SANs.
	IPs []net.IP
	// EKU is the ExtKeyUsage set; when nil it defaults to BOTH ServerAuth and
	// ClientAuth — a dual-purpose leaf usable as server or client, the shape the
	// original per-file helpers used. Set it explicitly to narrow the usage.
	EKU []x509.ExtKeyUsage
	// NotAfter overrides the leaf expiry. The zero value means
	// now+defaultLeafValidity; a past time produces an already-expired leaf
	// (for testing expiry handling).
	NotAfter time.Time
}

// Leaf is an issued leaf cert + key in the forms tests consume.
type Leaf struct {
	// Cert is the parsed leaf certificate.
	Cert *x509.Certificate
	// CertPEM is the PEM-encoded leaf certificate.
	CertPEM []byte
	// KeyPEM is the PEM-encoded PKCS#8 leaf private key.
	KeyPEM []byte
	// TLSCert is a ready-to-use tls.Certificate (Leaf field populated).
	TLSCert tls.Certificate
}

// IssueLeaf issues an ECDSA P-256 leaf signed by the CA per opts.
func (ca *CA) IssueLeaf(t *testing.T, opts LeafOptions) Leaf {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("tlsutiltest: generate leaf key: %v", err)
	}

	eku := opts.EKU
	if eku == nil {
		eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	}
	notAfter := opts.NotAfter
	if notAfter.IsZero() {
		notAfter = time.Now().Add(defaultLeafValidity)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(ca.serial.Add(1)),
		Subject:      pkix.Name{CommonName: "tlsutiltest-leaf"},
		NotBefore:    time.Now().Add(-certBackdate),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		URIs:         opts.URIs,
		DNSNames:     opts.DNSNames,
		IPAddresses:  opts.IPs,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("tlsutiltest: create leaf cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("tlsutiltest: parse leaf cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("tlsutiltest: marshal leaf key: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tlsutiltest: build tls.Certificate: %v", err)
	}
	tlsCert.Leaf = cert

	return Leaf{Cert: cert, CertPEM: certPEM, KeyPEM: keyPEM, TLSCert: tlsCert}
}

// SPIFFEURI parses raw into a *url.URL for use as a SPIFFE URI SAN — the cell-
// identity use case this package serves — failing the test on a parse error.
// It does NOT validate SPIFFE-ID format: raw is passed verbatim to url.Parse,
// so any parseable URI is accepted and the caller owns the scheme.
func SPIFFEURI(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("tlsutiltest: parse URI %q: %v", raw, err)
	}
	return u
}

// WriteFiles writes the leaf cert, leaf key, and CA cert as PEM files under dir
// (0o600) and returns their paths — for consumers configured by file path.
func (l Leaf) WriteFiles(t *testing.T, dir string, ca *CA) (certFile, keyFile, caFile string) {
	t.Helper()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	caFile = filepath.Join(dir, "ca.pem")
	// Fixed order; key owner-only (0o600), cert + CA public (0o644).
	writeFile(t, certFile, l.CertPEM, certFileMode)
	writeFile(t, keyFile, l.KeyPEM, keyFileMode)
	writeFile(t, caFile, ca.CertPEM, certFileMode)
	return certFile, keyFile, caFile
}

// writeFile writes data to path with mode, failing the test on error.
func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("tlsutiltest: write %s: %v", path, err)
	}
}
