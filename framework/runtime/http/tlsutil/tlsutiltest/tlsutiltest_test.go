package tlsutiltest_test

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
)

const testSPIFFE = "spiffe://example.org/cell/configcore"

// anyEKU lets Verify ignore EKU chaining so these tests assert SAN/expiry/curve
// without coupling to EKU verification semantics.
var anyEKU = []x509.ExtKeyUsage{x509.ExtKeyUsageAny}

func TestIssueLeaf_VerifiesAgainstCAPool(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, testSPIFFE)},
	})
	if _, err := leaf.Cert.Verify(x509.VerifyOptions{Roots: ca.Pool, KeyUsages: anyEKU}); err != nil {
		t.Fatalf("leaf must verify against CA pool: %v", err)
	}
	// Zero-value LeafOptions.NotAfter must default to a future expiry.
	if !leaf.Cert.NotAfter.After(time.Now()) {
		t.Fatalf("default-NotAfter leaf must be valid into the future, got NotAfter=%s", leaf.Cert.NotAfter)
	}
}

func TestIssueLeaf_SANsLandInCert(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs:     []*url.URL{tlsutiltest.SPIFFEURI(t, testSPIFFE)},
		DNSNames: []string{"localhost"},
		IPs:      []net.IP{net.ParseIP("127.0.0.1")},
	})
	if len(leaf.Cert.URIs) != 1 || leaf.Cert.URIs[0].String() != testSPIFFE {
		t.Fatalf("SPIFFE URI SAN missing: got %v", leaf.Cert.URIs)
	}
	if !slices.Contains(leaf.Cert.DNSNames, "localhost") {
		t.Fatalf("DNS SAN missing: got %v", leaf.Cert.DNSNames)
	}
	if len(leaf.Cert.IPAddresses) != 1 || !leaf.Cert.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("IP SAN missing: got %v", leaf.Cert.IPAddresses)
	}
}

func TestIssueLeaf_EKUDefaultAndExplicit(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)

	def := ca.IssueLeaf(t, tlsutiltest.LeafOptions{})
	if !slices.Equal(def.Cert.ExtKeyUsage,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}) {
		t.Fatalf("default EKU must be Server+Client, got %v", def.Cert.ExtKeyUsage)
	}

	explicit := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if !slices.Equal(explicit.Cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Fatalf("explicit EKU must be exactly ClientAuth, got %v", explicit.Cert.ExtKeyUsage)
	}
}

func TestIssueLeaf_NotAfterPastIsExpired(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		NotAfter: time.Now().Add(-time.Minute),
	})
	_, err := leaf.Cert.Verify(x509.VerifyOptions{Roots: ca.Pool, KeyUsages: anyEKU})
	if err == nil {
		t.Fatal("expired leaf must fail verification")
	}
}

func TestIssueLeaf_TLSCertUsable(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{DNSNames: []string{"localhost"}})
	if leaf.TLSCert.Leaf == nil {
		t.Fatal("TLSCert.Leaf must be populated")
	}
	if len(leaf.TLSCert.Certificate) != 1 {
		t.Fatalf("TLSCert must carry one cert, got %d", len(leaf.TLSCert.Certificate))
	}
	// Must be loadable into a tls.Config without error.
	_ = &tls.Config{Certificates: []tls.Certificate{leaf.TLSCert}, MinVersion: tls.VersionTLS13}
}

func TestLeaf_WriteFilesReadable(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, testSPIFFE)},
	})
	certFile, keyFile, caFile := leaf.WriteFiles(t, t.TempDir(), ca)

	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("written cert/key must load: %v", err)
	}
	caPEM, err := os.ReadFile(caFile) //nolint:gosec // G304: caFile is a t.TempDir() path produced by WriteFiles in this test
	if err != nil {
		t.Fatalf("read ca file: %v", err)
	}
	if _, err := tlsutil.NewClientCAPool(caPEM); err != nil {
		t.Fatalf("written CA must parse into a pool: %v", err)
	}
	// The private key file must be owner-only (0o600). umask only clears bits,
	// and 0o600 has no group/other bits to clear, so this is umask-robust.
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("key file mode = %o, want 0600 (owner-only private key)", got)
	}
}

func TestIssueLeaf_CurveIsECDSA(t *testing.T) {
	t.Parallel()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{})
	if leaf.Cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("leaf must be ECDSA, got %v", leaf.Cert.PublicKeyAlgorithm)
	}
}

func TestNewCA_DistinctCertsAcrossInstances(t *testing.T) {
	t.Parallel()
	// Two CAs must produce distinct certs so x509.CertPool.Equal can tell pools
	// apart (the adapters/mqtt config_test pool-defensive-copy use case).
	a := tlsutiltest.NewCA(t)
	b := tlsutiltest.NewCA(t)
	if a.Cert.Equal(b.Cert) {
		t.Fatal("independent CAs must yield distinct certs")
	}
}

func TestIssueLeaf_DistinctSerialsWithinCA(t *testing.T) {
	t.Parallel()
	// One CA issuing multiple leaves must give each a distinct serial (RFC 5280
	// §4.1.2.2) and none may collide with the root's serial.
	ca := tlsutiltest.NewCA(t)
	a := ca.IssueLeaf(t, tlsutiltest.LeafOptions{})
	b := ca.IssueLeaf(t, tlsutiltest.LeafOptions{})
	if a.Cert.SerialNumber.Cmp(b.Cert.SerialNumber) == 0 {
		t.Fatalf("leaves from the same CA must have distinct serials, both = %s", a.Cert.SerialNumber)
	}
	if a.Cert.SerialNumber.Cmp(ca.Cert.SerialNumber) == 0 || b.Cert.SerialNumber.Cmp(ca.Cert.SerialNumber) == 0 {
		t.Fatalf("leaf serial must differ from the root serial %s", ca.Cert.SerialNumber)
	}
}
