package reconcile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNewLeaseTTL covers the lease-window constructor's fail-closed boundary: any
// sub-millisecond duration — which Milliseconds() would truncate to 0, yielding an
// instantly-expiring lease and multiple live leaders — is rejected, and a valid
// duration is floored to integer-millisecond wire granularity with ms >= 1
// guaranteed (so a constructed LeaseTTL can never re-introduce the truncation).
func TestNewLeaseTTL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      time.Duration
		wantErr bool
		wantMS  int64 // only checked when !wantErr
	}{
		{"one_nanosecond_rejected", time.Nanosecond, true, 0},
		{"sub_millisecond_999us_rejected", 999 * time.Microsecond, true, 0},
		{"zero_rejected", 0, true, 0},
		{"negative_rejected", -time.Second, true, 0},
		{"exactly_1ms_ok", time.Millisecond, false, 1},
		{"1500us_floors_to_1ms", 1500 * time.Microsecond, false, 1},
		{"30s_ok", 30 * time.Second, false, 30_000},
		{"one_hour_ok", time.Hour, false, 3_600_000},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewLeaseTTL(tt.in)
			if tt.wantErr {
				require.Error(t, err, "sub-ms / non-positive lease TTL must fail-closed")
				require.True(t, got.IsZero(), "a rejected constructor returns the zero value")
				return
			}
			require.NoError(t, err)
			require.False(t, got.IsZero(), "a constructed lease TTL is never the zero value")
			require.Equal(t, tt.wantMS, got.Milliseconds(), "Milliseconds() is the integer-ms, floored window")
			require.GreaterOrEqual(t, got.Milliseconds(), int64(1), "a constructed lease TTL is never sub-ms")
			require.Equal(t, time.Duration(tt.wantMS)*time.Millisecond, got.Duration(), "Duration() reflects the ms-floored window")
		})
	}
}

// TestLeaseTTL_ZeroValue documents the irreducible escape of a value type: the
// unconstructed zero value LeaseTTL{} has ms==0, which IsZero reports so elector
// constructors can fail-fast on a forgot-to-construct caller — a loud, obvious
// error, never the silent sub-ms truncation NewLeaseTTL forecloses.
func TestLeaseTTL_ZeroValue(t *testing.T) {
	t.Parallel()
	var zero LeaseTTL
	require.True(t, zero.IsZero(), "the zero value is the unconstructed sentinel")
	require.Equal(t, int64(0), zero.Milliseconds())
	require.Equal(t, time.Duration(0), zero.Duration())
}
