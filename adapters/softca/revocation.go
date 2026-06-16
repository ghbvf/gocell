package softca

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// crlValidity is the CRL nextUpdate window from thisUpdate.
const crlValidity = 7 * 24 * time.Hour

// RevocationStore is softca's [certsigning.RevocationStore]: it records and
// enumerates revocations through the shared issuance [Ledger] (so cross-scope
// isolation and Tidy expiry come from the recorded issuance facts) and signs DER
// CRLs with the CA's intermediate key.
type RevocationStore struct {
	clk    clock.Clock
	ca     *CA
	ledger Ledger
}

// compile-time conformance to the seam.
var _ certsigning.RevocationStore = (*RevocationStore)(nil)

// NewRevocationStore builds a RevocationStore. Pass the SAME [Ledger] instance
// as the [Signer] so revocation sees the certificates the signer issued.
func NewRevocationStore(clk clock.Clock, ca *CA, ledger Ledger) (*RevocationStore, error) {
	clock.MustHaveClock(clk, "softca.NewRevocationStore")
	if ca == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "softca: ca required")
	}
	if validation.IsNilInterface(ledger) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "softca: ledger required")
	}
	return &RevocationStore{clk: clk, ca: ca, ledger: ledger}, nil
}

// Revoke marks serial revoked within scope. A serial not issued within scope
// fails closed (cross-scope / unknown → not found).
func (s *RevocationStore) Revoke(
	ctx context.Context,
	scope certsigning.CertScope,
	serial certsigning.Serial,
	reason certsigning.RevocationReason,
) error {
	return s.ledger.Revoke(ctx, scope, serial, reason, s.clk.Now())
}

// RevocationList returns the revoked certificates within scope.
func (s *RevocationStore) RevocationList(ctx context.Context, scope certsigning.CertScope) ([]certsigning.RevokedCertificate, error) {
	return s.ledger.Revoked(ctx, scope)
}

// Tidy removes revoked records within scope whose certificate expired before the instant.
func (s *RevocationStore) Tidy(ctx context.Context, scope certsigning.CertScope, before time.Time) error {
	return s.ledger.Tidy(ctx, scope, before)
}

// GenerateCRL builds and signs a DER CRL (RFC 5280) for scope, signed by the CA
// intermediate key — the adapter extension over the seam (the seam's
// RevocationList returns structured entries; a signed CRL needs the CA key, held
// here). It is not part of [certsigning.RevocationStore].
func (s *RevocationStore) GenerateCRL(ctx context.Context, scope certsigning.CertScope) ([]byte, error) {
	revoked, err := s.ledger.Revoked(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, rc := range revoked {
		sn, ok := new(big.Int).SetString(rc.Serial.String(), 16)
		if !ok {
			return nil, errCRLFailed("revoked serial not hexadecimal", nil)
		}
		entries = append(entries, x509.RevocationListEntry{
			SerialNumber:   sn,
			RevocationTime: rc.RevokedAt,
			ReasonCode:     reasonCode(rc.Reason),
		})
	}
	now := s.clk.Now()
	tmpl := &x509.RevocationList{
		Number:                    big.NewInt(now.UnixNano()),
		ThisUpdate:                now,
		NextUpdate:                now.Add(crlValidity),
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, s.ca.interCert, s.ca.interKey)
	if err != nil {
		return nil, errCRLFailed("create revocation list failed", err)
	}
	return der, nil
}

// reasonCode maps a sealed [certsigning.RevocationReason] to its RFC 5280 §5.3.1
// numeric CRL reason code. It compares against the seam's reason singletons (not
// hardcoded mnemonics) so it stays correct if a mnemonic string is reworded. The
// zero/unspecified reason returns 0 (Go omits the reason extension for 0).
func reasonCode(r certsigning.RevocationReason) int {
	switch {
	case r == certsigning.ReasonKeyCompromise():
		return 1
	case r == certsigning.ReasonCACompromise():
		return 2
	case r == certsigning.ReasonAffiliationChanged():
		return 3
	case r == certsigning.ReasonSuperseded():
		return 4
	case r == certsigning.ReasonCessationOfOperation():
		return 5
	case r == certsigning.ReasonCertificateHold():
		return 6
	case r == certsigning.ReasonRemoveFromCRL():
		return 8
	case r == certsigning.ReasonPrivilegeWithdrawn():
		return 9
	case r == certsigning.ReasonAACompromise():
		return 10
	default:
		return 0 // unspecified
	}
}
