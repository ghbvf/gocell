package softca_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/softca"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

func TestMemLedger_EpochMonotonicPerScope(t *testing.T) {
	t.Parallel()
	ledger := softca.NewMemLedger()
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")
	notAfter := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for want := uint64(0); want < 3; want++ {
		serial, err := cs.NewSerial(hexN(want))
		require.NoError(t, err)
		got, err := ledger.Record(ctx, scope, serial, notAfter)
		require.NoError(t, err)
		require.Equal(t, want, got, "epoch must increment per issuance")
	}

	// A different scope starts its own epoch sequence at 0.
	other := mustScope(t, testTenantB, "device-1")
	serial, err := cs.NewSerial("01")
	require.NoError(t, err)
	got, err := ledger.Record(ctx, other, serial, notAfter)
	require.NoError(t, err)
	require.Equal(t, uint64(0), got)
}

func TestMemLedger_RevokeUnknownScopeFailsClosed(t *testing.T) {
	t.Parallel()
	ledger := softca.NewMemLedger()
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")
	serial, err := cs.NewSerial("0a")
	require.NoError(t, err)

	err = ledger.Revoke(ctx, scope, serial, cs.ReasonUnspecified(), time.Now())
	require.Error(t, err, "revoking in a scope with no issuances fails closed")
}

func TestMemLedger_RevokedSortedAndUTC(t *testing.T) {
	t.Parallel()
	ledger := softca.NewMemLedger()
	ctx := context.Background()
	scope := mustScope(t, testTenant, "device-1")
	notAfter := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 3600))

	for _, s := range []string{"0c", "0a", "0b"} {
		serial, err := cs.NewSerial(s)
		require.NoError(t, err)
		_, err = ledger.Record(ctx, scope, serial, notAfter)
		require.NoError(t, err)
		require.NoError(t, ledger.Revoke(ctx, scope, serial, cs.ReasonSuperseded(), at))
	}

	list, err := ledger.Revoked(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, []string{"0a", "0b", "0c"}, []string{
		list[0].Serial.String(), list[1].Serial.String(), list[2].Serial.String(),
	}, "revoked entries must be sorted by serial")
	require.Equal(t, time.UTC, list[0].RevokedAt.Location(), "RevokedAt normalized to UTC")
}

// hexN renders a small uint64 as a non-empty hex serial string.
func hexN(n uint64) string {
	const digits = "0123456789abcdef"
	if n == 0 {
		return "00"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{digits[n&0xf]}, b...)
		n >>= 4
	}
	return string(b)
}
