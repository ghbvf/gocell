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

// TestSign_ChainVerifies is the headline scenario: a leaf minted by softca
// verifies against the trust bundle (leaf → intermediate → root), and its
// subject / SANs come from the request, NOT the throwaway CSR.
func TestSign_ChainVerifies(t *testing.T) {
	t.Parallel()
	signer, _, clk := newProvider(t)
	ctx := context.Background()

	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf-cn", time.Hour))
	require.NoError(t, err)

	leaf, err := issued.Certificate()
	require.NoError(t, err)

	bundle, err := signer.TrustBundle(ctx)
	require.NoError(t, err)
	intermediate, root := parseTrustBundle(t, bundle)

	roots := x509.NewCertPool()
	roots.AddCert(root)
	inters := x509.NewCertPool()
	inters.AddCert(intermediate)

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		CurrentTime:   clk.Now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err, "issued chain must verify against the trust bundle")

	require.Equal(t, "leaf-cn", leaf.Subject.CommonName, "subject must come from the request, not the CSR")
	require.NotEqual(t, csrCN, leaf.Subject.CommonName)
	require.Equal(t, []string{"device-1.example.com"}, leaf.DNSNames, "SANs must come from the request")
}

// TestSign_RenewalEpochIncrements covers "续期 epoch+1": re-signing the same
// scope advances the epoch 0 → 1 and the renewed cert outlives the first.
func TestSign_RenewalEpochIncrements(t *testing.T) {
	t.Parallel()
	signer, _, clk := newProvider(t)
	ctx := context.Background()

	first, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	require.Equal(t, uint64(0), first.Epoch(), "initial enrollment epoch is 0")

	clk.Advance(renewGap)

	second, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	require.Equal(t, uint64(1), second.Epoch(), "renewal epoch is prior+1")
	require.True(t, second.NotAfter().After(first.NotAfter()), "renewed cert must outlive the prior one")

	// A different device under the same tenant is a distinct scope → epoch resets to 0.
	other, err := signer.Sign(ctx, authedRequest(t, "device-2", "leaf", time.Hour))
	require.NoError(t, err)
	require.Equal(t, uint64(0), other.Epoch(), "epoch is per-scope, not global")
}

// TestSign_TTLClampedToIssuerExpiry covers "TTL clamp": a leaf cannot outlive
// its issuing intermediate CA, even when the (authorized) request asks for more.
func TestSign_TTLClampedToIssuerExpiry(t *testing.T) {
	t.Parallel()
	signer, _, clk := newProvider(t)
	ctx := context.Background()

	bundle, err := signer.TrustBundle(ctx)
	require.NoError(t, err)
	intermediate, _ := parseTrustBundle(t, bundle)

	hugeTTL := beyondCALifetime // far beyond the intermediate's lifetime
	issued, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", hugeTTL))
	require.NoError(t, err)

	require.True(t, issued.NotAfter().Equal(intermediate.NotAfter),
		"leaf notAfter must be clamped to the issuing CA expiry")
	require.True(t, issued.NotAfter().Before(clk.Now().Add(hugeTTL)),
		"clamped notAfter must be earlier than the requested lifetime")
}

func TestNewSigner_NilDependenciesFailFast(t *testing.T) {
	t.Parallel()
	ca, clk := newCA(t)

	_, err := softca.NewSigner(clk, nil, softca.NewMemLedger())
	require.Error(t, err, "nil ca must fail fast")

	_, err = softca.NewSigner(clk, ca, nil)
	require.Error(t, err, "nil ledger must fail fast")
}

func TestNewSigner_NilClockPanics(t *testing.T) {
	t.Parallel()
	ca, _ := newCA(t)
	require.Panics(t, func() {
		_, _ = softca.NewSigner(nil, ca, softca.NewMemLedger())
	}, "nil clock must panic per clock.MustHaveClock")
}

// TestSign_DistinctSerialsAndLedgerLinkage asserts each issuance gets a distinct
// serial and that the issued serial is the one the revocation ledger can locate
// (the signer records the same serial the IssuedCert derives from its DER).
func TestSign_DistinctSerialsAndLedgerLinkage(t *testing.T) {
	t.Parallel()
	signer, revs, _ := newProvider(t)
	ctx := context.Background()

	a, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	b, err := signer.Sign(ctx, authedRequest(t, "device-1", "leaf", time.Hour))
	require.NoError(t, err)
	require.NotEqual(t, a.Serial().String(), b.Serial().String(), "serials must be unique")

	// The signer-recorded serial is revocable (proves signer↔ledger serial linkage).
	require.NoError(t, revs.Revoke(ctx, mustScope(t, testTenant, "device-1"), a.Serial(), cs.ReasonSuperseded()))
}

// compile-time assertion mirrored in test space.
var _ cs.Signer = (*softca.Signer)(nil)
