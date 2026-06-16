package softca

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// TestReasonCode_MapsAllRFCReasons pins the RFC 5280 §5.3.1 reason→code mapping
// for every sealed reason plus the zero value (→ unspecified).
func TestReasonCode_MapsAllRFCReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason cs.RevocationReason
		want   int
	}{
		{cs.ReasonUnspecified(), 0},
		{cs.ReasonKeyCompromise(), 1},
		{cs.ReasonCACompromise(), 2},
		{cs.ReasonAffiliationChanged(), 3},
		{cs.ReasonSuperseded(), 4},
		{cs.ReasonCessationOfOperation(), 5},
		{cs.ReasonCertificateHold(), 6},
		{cs.ReasonRemoveFromCRL(), 8},
		{cs.ReasonPrivilegeWithdrawn(), 9},
		{cs.ReasonAACompromise(), 10},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, reasonCode(c.reason), "reason %s", c.reason.String())
	}
	require.Equal(t, 0, reasonCode(cs.RevocationReason{}), "zero reason → unspecified")
}

// TestErrorHelpers_CodesAndKinds pins each softca error helper to its
// provider-agnostic ERR_CERT_ code and HTTP-mapping Kind.
func TestErrorHelpers_CodesAndKinds(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	cases := []struct {
		name string
		err  error
		code errcode.Code
		kind errcode.Kind
	}{
		{"ca-init", errCAInit("x", cause), errcode.ErrCertCAInit, errcode.KindInternal},
		{"sign-failed", errSignFailed("x", cause), errcode.ErrCertSignFailed, errcode.KindInternal},
		{"crl-failed", errCRLFailed("x", cause), errcode.ErrCertCRLFailed, errcode.KindInternal},
		{"revoke-not-found", errRevokeNotFound("x"), errcode.ErrCertRevokeNotFound, errcode.KindNotFound},
		{"revoke-unsupported", errRevokeUnsupported("x"), errcode.ErrCertRevokeUnsupported, errcode.KindInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var e *errcode.Error
			require.True(t, errors.As(c.err, &e), "must be *errcode.Error")
			require.Equal(t, c.code, e.Code)
			require.Equal(t, c.kind, e.Kind)
		})
	}
}
