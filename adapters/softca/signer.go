package softca

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// Signer is softca's [certsigning.Signer]: it mints leaf device certificates
// with the standard library (x509.CreateCertificate), signed by the CA's
// intermediate key, and stamps the per-scope renewal epoch from the issuance
// [Ledger]. The CA signing key never leaves this adapter.
type Signer struct {
	clk    clock.Clock
	ca     *CA
	ledger Ledger
}

// compile-time conformance to the seam.
var _ certsigning.Signer = (*Signer)(nil)

// NewSigner builds a Signer. clk is the positional clock (clock.MustHaveClock);
// ca and ledger are required (a nil dependency fails fast). Inject a persistent
// Ledger to retain renewal epochs across restarts; [NewMemLedger] is the dev default.
//
// The ledger MUST be the SAME instance passed to [NewRevocationStore] — revocation
// can only see certificates this Signer recorded. Prefer [NewSoftCA], which wires
// both halves over one Ledger so they cannot diverge.
func NewSigner(clk clock.Clock, ca *CA, ledger Ledger) (*Signer, error) {
	clock.MustHaveClock(clk, "softca.NewSigner")
	if ca == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "softca: ca required")
	}
	if validation.IsNilInterface(ledger) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "softca: ledger required")
	}
	return &Signer{clk: clk, ca: ca, ledger: ledger}, nil
}

// Sign mints a certificate for an authorized request. Authorization and its
// constraints (TTL ≤ grant, SAN ⊆ allowance) were enforced when the
// [certsigning.AuthorizedCertRequest] was constructed; here softca takes the
// public key from the CSR and the subject / SANs / usages from the (constrained)
// request — never blindly from the CSR — and clamps notAfter to the issuing CA's
// expiry (a leaf cannot outlive its issuer). Fail-closed: any signing failure
// returns an error, never a partial credential.
func (s *Signer) Sign(ctx context.Context, authReq certsigning.AuthorizedCertRequest) (certsigning.IssuedCert, error) {
	req := authReq.Request()
	// POP (CSR self-signature) was verified by certsigning.NewCertRequest; we
	// only need the subject public key here.
	csr, err := x509.ParseCertificateRequest(req.CSRDER())
	if err != nil {
		return certsigning.IssuedCert{}, errSignFailed("csr parse failed", err)
	}
	now := s.clk.Now()
	notAfter := now.Add(req.TTL())
	if notAfter.After(s.ca.interCert.NotAfter) {
		notAfter = s.ca.interCert.NotAfter // clamp: leaf must not outlive its issuing CA
	}
	serial, err := randomSerial()
	if err != nil {
		return certsigning.IssuedCert{}, errSignFailed("serial generation failed", err)
	}
	sans := req.SubjectAltNames()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: req.Subject().CommonName()},
		NotBefore:             now,
		NotAfter:              notAfter,
		KeyUsage:              req.KeyUsages().KeyUsage(),
		ExtKeyUsage:           req.KeyUsages().ExtKeyUsage(),
		DNSNames:              sans.DNSNames(),
		IPAddresses:           sans.IPAddresses(),
		URIs:                  sans.URIs(),
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.ca.interCert, csr.PublicKey, s.ca.interKey)
	if err != nil {
		return certsigning.IssuedCert{}, errSignFailed("create certificate failed", err)
	}
	issuedSerial, err := certsigning.NewSerial(serial.Text(16))
	if err != nil {
		return certsigning.IssuedCert{}, errSignFailed("serial encode failed", err)
	}
	epoch, err := s.ledger.Record(ctx, req.Scope(), issuedSerial, notAfter)
	if err != nil {
		return certsigning.IssuedCert{}, errSignFailed("issuance ledger record failed", err)
	}
	return certsigning.NewIssuedCert(req.Scope(), der, [][]byte{s.ca.interDER}, epoch)
}

// TrustBundle returns the CA trust anchors (intermediate first, root last).
func (s *Signer) TrustBundle(_ context.Context) ([][]byte, error) {
	return s.ca.trustBundle(), nil
}
