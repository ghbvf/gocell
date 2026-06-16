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
	require.Equal(t, 1, crl.RevokedCertificateEntries[0].ReasonCode,
		"reason code must round-trip end-to-end (keyCompromise = 1)")

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
	clk.Advance(tidyAdvance)
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

func TestNewRevocationStore_NilClockPanics(t *testing.T) {
	t.Parallel()
	ca, _ := newCA(t)
	require.Panics(t, func() {
		_, _ = softca.NewRevocationStore(nil, ca, softca.NewMemLedger())
	}, "nil clock must panic per clock.MustHaveClock")
}

// TestGenerateCRL_NumberMonotonic asserts the CRL Number strictly increases
// across regenerations even when the clock does NOT advance (RFC 5280 §5.2.3) —
// the regression guard for the former wall-clock-derived Number.
func TestGenerateCRL_NumberMonotonic(t *testing.T) {
	t.Parallel()
	signer, revs, _ := newProvider(t)
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")

	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	require.NoError(t, revs.Revoke(ctx, scope, issued.Serial(), cs.ReasonKeyCompromise()))

	first, err := revs.GenerateCRL(ctx, scope) // clock frozen — same instant
	require.NoError(t, err)
	second, err := revs.GenerateCRL(ctx, scope)
	require.NoError(t, err)

	c1, err := x509.ParseRevocationList(first)
	require.NoError(t, err)
	c2, err := x509.ParseRevocationList(second)
	require.NoError(t, err)
	require.Positive(t, c2.Number.Cmp(c1.Number), "CRL Number must strictly increase even without clock advance")
}

// TestNewSoftCA_SharedLedgerWires asserts the bundle constructor produces a
// Signer + RevocationStore that share one ledger (revoke sees the signed cert).
func TestNewSoftCA_SharedLedgerWires(t *testing.T) {
	t.Parallel()
	ca, clk := newCA(t)
	signer, revs, err := softca.NewSoftCA(clk, ca, softca.NewMemLedger())
	require.NoError(t, err)

	ctx := context.Background()
	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	// Shared ledger → the revocation store can revoke the signer's serial.
	require.NoError(t, revs.Revoke(ctx, mustScope(t, testTenant, "device-1"), issued.Serial(), cs.ReasonSuperseded()))
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
