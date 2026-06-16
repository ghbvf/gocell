package softca_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/softca"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

const (
	testTenant  = "11111111-1111-1111-1111-111111111111"
	testTenantB = "22222222-2222-2222-2222-222222222222"
	csrCN       = "csr-common-name-ignored"
)

// Test-time durations extracted to package-level consts (TEST-TIME-LITERAL-01:
// no inline N*time.X at call sites).
const (
	renewGap         = 30 * time.Minute           // clock advance between two issuances
	tidyAdvance      = 2 * time.Hour              // clock advance past a 1h leaf TTL for Tidy
	beyondCALifetime = 100 * 365 * 24 * time.Hour // a TTL far past the intermediate's lifetime
)

// newCA builds an in-memory dev CA at a fixed epoch for deterministic tests.
func newCA(t *testing.T) (*softca.CA, *clockmock.FakeClock) {
	t.Helper()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ca, err := softca.NewDevCA(clk)
	require.NoError(t, err)
	return ca, clk
}

// newProvider wires a Signer + RevocationStore sharing one in-memory Ledger.
func newProvider(t *testing.T) (*softca.Signer, *softca.RevocationStore, *clockmock.FakeClock) {
	t.Helper()
	ca, clk := newCA(t)
	ledger := softca.NewMemLedger()
	signer, err := softca.NewSigner(clk, ca, ledger)
	require.NoError(t, err)
	revs, err := softca.NewRevocationStore(clk, ca, ledger)
	require.NoError(t, err)
	return signer, revs, clk
}

func mustTenant(t *testing.T, s string) tenant.TenantID {
	t.Helper()
	tid, err := tenant.ParseTenantID(s)
	require.NoError(t, err)
	return tid
}

func mustScope(t *testing.T, tenantID, device string) cs.CertScope {
	t.Helper()
	iss, err := cs.NewIssuerID("ca-root")
	require.NoError(t, err)
	dev, err := cs.NewDeviceID(device)
	require.NoError(t, err)
	scope, err := cs.NewCertScope(mustTenant(t, tenantID), iss, dev)
	require.NoError(t, err)
	return scope
}

// testCSRDER fabricates a valid, self-signed PKCS#10 CSR DER (POP passes). Its
// common name is deliberately a throwaway: softca takes the subject from the
// request, never the CSR.
func testCSRDER(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: csrCN}}, key)
	require.NoError(t, err)
	return der
}

// authedRequest builds an AuthorizedCertRequest for device under the default
// test tenant with the given leaf TTL. The grant allows the request's SANs and a
// TTL ceiling above ttl, so authorization passes and only softca's behavior is
// under test. (Cross-tenant cases build their scope directly via mustScope.)
func authedRequest(t *testing.T, device, commonName string, ttl time.Duration) cs.AuthorizedCertRequest {
	t.Helper()
	scope := mustScope(t, testTenant, device)
	dev, err := cs.NewDeviceID(device)
	require.NoError(t, err)
	subject, err := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, commonName)
	require.NoError(t, err)
	usages, err := cs.NewKeyUsages(x509.KeyUsageDigitalSignature, x509.ExtKeyUsageClientAuth)
	require.NoError(t, err)
	sans, err := cs.NewSubjectAltNames([]string{device + ".example.com"}, nil, nil)
	require.NoError(t, err)
	grant, err := cs.NewSignConstraints(ttl+time.Hour, sans)
	require.NoError(t, err)
	req, err := cs.NewCertRequest(scope, subject, testCSRDER(t), sans, usages, ttl)
	require.NoError(t, err)
	authed, err := cs.NewAuthorizedCertRequest(req, grant)
	require.NoError(t, err)
	return authed
}

// mustBigSerial parses a lowercase-hex serial into a *big.Int for CRL comparison.
func mustBigSerial(t *testing.T, hexSerial string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(hexSerial, 16)
	require.True(t, ok, "serial %q must be hex", hexSerial)
	return n
}

// parseTrustBundle returns (intermediate, root) parsed from a Signer trust bundle
// (intermediate first, root last).
func parseTrustBundle(t *testing.T, bundle [][]byte) (intermediate, root *x509.Certificate) {
	t.Helper()
	require.Len(t, bundle, 2, "trust bundle must be [intermediate, root]")
	var err error
	intermediate, err = x509.ParseCertificate(bundle[0])
	require.NoError(t, err)
	root, err = x509.ParseCertificate(bundle[1])
	require.NoError(t, err)
	return intermediate, root
}
