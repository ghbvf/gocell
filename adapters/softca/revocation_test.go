package softca_test

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/softca"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// TestRevoke_EntersCRL covers "吊销入 CRL": a revoked serial appears in both the
// structured RevocationList and the signed DER CRL, and the CRL verifies against
// the issuing intermediate.
func TestRevoke_EntersCRL(t *testing.T) {
	t.Parallel()
	signer, revs, _ := newProvider(t)
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")

	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)

	require.NoError(t, revs.Revoke(ctx, scope, issued.Serial(), cs.ReasonKeyCompromise()))

	list, err := revs.RevocationList(ctx, scope)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, issued.Serial().String(), list[0].Serial.String())
	require.Equal(t, cs.ReasonKeyCompromise(), list[0].Reason)
	require.Equal(t, time.UTC, list[0].RevokedAt.Location(), "RevokedAt must be UTC")

	crlDER, err := revs.GenerateCRL(ctx, scope)
	require.NoError(t, err)

	crl, err := x509.ParseRevocationList(crlDER)
	require.NoError(t, err)
	require.Len(t, crl.RevokedCertificateEntries, 1)
	require.Equal(t, 0, crl.RevokedCertificateEntries[0].SerialNumber.Cmp(
		mustBigSerial(t, issued.Serial().String())), "CRL must list the revoked serial")

	// CRL is signed by the issuing intermediate.
	bundle, err := signer.TrustBundle(ctx)
	require.NoError(t, err)
	intermediate, _ := parseTrustBundle(t, bundle)
	require.NoError(t, crl.CheckSignatureFrom(intermediate), "CRL must be signed by the intermediate CA")
}

// TestRevoke_CrossScopeFailsClosed asserts a serial issued under tenant A cannot
// be revoked or enumerated under tenant B's scope (绝不凭裸 serial 跨隔离域).
func TestRevoke_CrossScopeFailsClosed(t *testing.T) {
	t.Parallel()
	signer, revs, _ := newProvider(t)
	ctx := context.Background()

	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)

	// Same device id, different tenant → different isolation scope.
	otherScope := mustScope(t, testTenantB, "device-1")
	err = revs.Revoke(ctx, otherScope, issued.Serial(), cs.ReasonSuperseded())
	require.Error(t, err, "revoking another scope's serial must fail closed")

	list, err := revs.RevocationList(ctx, otherScope)
	require.NoError(t, err)
	require.Empty(t, list, "the other scope sees no revocations")
}

// TestTidy_DropsExpiredRevocations covers Tidy: once a revoked cert has expired,
// Tidy drops it from the revocation list / CRL.
func TestTidy_DropsExpiredRevocations(t *testing.T) {
	t.Parallel()
	signer, revs, clk := newProvider(t)
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")

	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	require.NoError(t, revs.Revoke(ctx, scope, issued.Serial(), cs.ReasonCessationOfOperation()))

	// Before expiry: tidy is a no-op.
	require.NoError(t, revs.Tidy(ctx, scope, clk.Now()))
	list, err := revs.RevocationList(ctx, scope)
	require.NoError(t, err)
	require.Len(t, list, 1, "an unexpired revocation survives tidy")

	// Advance past expiry, then tidy drops it.
	clk.Advance(2 * time.Hour)
	require.NoError(t, revs.Tidy(ctx, scope, clk.Now()))
	list, err = revs.RevocationList(ctx, scope)
	require.NoError(t, err)
	require.Empty(t, list, "an expired revocation is tidied away")
}

func TestNewRevocationStore_NilDependenciesFailFast(t *testing.T) {
	t.Parallel()
	ca, clk := newCA(t)

	_, err := softca.NewRevocationStore(clk, nil, softca.NewMemLedger())
	require.Error(t, err)

	_, err = softca.NewRevocationStore(clk, ca, nil)
	require.Error(t, err)
}

// TestRevoke_UnknownSerialFailsClosed asserts revoking a never-issued serial in a
// known scope fails closed.
func TestRevoke_UnknownSerialFailsClosed(t *testing.T) {
	t.Parallel()
	signer, revs, _ := newProvider(t)
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")

	// Issue one cert so the scope exists in the ledger.
	_, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)

	unknown, err := cs.NewSerial("deadbeef")
	require.NoError(t, err)
	require.Error(t, revs.Revoke(ctx, scope, unknown, cs.ReasonUnspecified()),
		"revoking a serial never issued in scope must fail closed")
}

var _ cs.RevocationStore = (*softca.RevocationStore)(nil)
