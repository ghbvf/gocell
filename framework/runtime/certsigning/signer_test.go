package certsigning_test

import (
	"context"
	"crypto/x509"
	"math/big"
	"testing"
	"time"

	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// fakeSigner is a TEST-ONLY Signer demonstrating that the seam expresses a
// CSR→IssuedCert mint plus a trust bundle, with the issued serial / validity
// derived from the certificate DER (single-sourced). It is NOT a CA — it
// fabricates a self-signed DER to exercise the interface contract. The real
// implementation (adapters/softca) lands in PR-6.
type fakeSigner struct {
	t        *testing.T
	notAfter time.Time
}

func (f fakeSigner) Sign(_ context.Context, req cs.CertRequest) (cs.IssuedCert, error) {
	der := testCertDER(f.t, big.NewInt(0x42), f.notAfter)
	return cs.NewIssuedCert(req.Scope(), der, nil, 0)
}

func (fakeSigner) TrustBundle(context.Context) ([][]byte, error) {
	return [][]byte{[]byte("ca-der")}, nil
}

func TestSignerContract(t *testing.T) {
	t.Parallel()
	notAfter := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	var signer cs.Signer = fakeSigner{t: t, notAfter: notAfter}

	scope := mustScope(t)
	dev, _ := cs.NewDeviceID("device-1")
	subject, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "device-1")
	usages, _ := cs.NewKeyUsages(x509.KeyUsageDigitalSignature, x509.ExtKeyUsageClientAuth)
	sans, _ := cs.NewSubjectAltNames([]string{"device-1.example"}, nil, nil)
	req, err := cs.NewCertRequest(scope, subject, testCSRDER(t, "device-1"), sans, usages, time.Hour)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	issued, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if issued.Serial().String() != "42" {
		t.Errorf("issued serial = %q, want 42", issued.Serial().String())
	}
	if !issued.NotAfter().Equal(notAfter) {
		t.Errorf("issued notAfter = %v", issued.NotAfter())
	}
	if !issued.Scope().Equal(scope) {
		t.Error("issued scope must equal request scope")
	}

	bundle, err := signer.TrustBundle(context.Background())
	if err != nil || len(bundle) != 1 {
		t.Fatalf("trust bundle: %v len=%d", err, len(bundle))
	}
}
