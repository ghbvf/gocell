package reconcile

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// const-extracted test durations (TEST-TIME-LITERAL-01).
const (
	gettersExplicitInterval = 7 * time.Second
	gettersRenewInterval    = 3 * time.Second
	gettersTokenSpan        = 9 * time.Second
)

// TestLoop_Getters covers the unexported config-getter helpers' both branches
// (explicit field vs. default/sentinel) without standing up a running Loop.
func TestLoop_Getters(t *testing.T) {
	t.Run("interval_explicit", func(t *testing.T) {
		l := &Loop{Interval: gettersExplicitInterval}
		require.Equal(t, gettersExplicitInterval, l.interval())
	})
	t.Run("interval_default", func(t *testing.T) {
		require.Equal(t, defaultReconcileInterval, (&Loop{}).interval())
	})

	t.Run("reconcilerID_explicit", func(t *testing.T) {
		require.Equal(t, "abc", (&Loop{ReconcilerID: "abc"}).reconcilerID())
	})
	t.Run("reconcilerID_sentinel", func(t *testing.T) {
		require.Equal(t, reconcilerIDSentinel, (&Loop{}).reconcilerID())
	})

	t.Run("name_explicit", func(t *testing.T) {
		require.Equal(t, "myloop", (&Loop{Name: "myloop"}).name())
	})
	t.Run("name_default", func(t *testing.T) {
		require.Equal(t, defaultLoopName, (&Loop{}).name())
	})

	t.Run("logger_explicit", func(t *testing.T) {
		custom := slog.New(slog.NewTextHandler(io.Discard, nil))
		require.Same(t, custom, (&Loop{Logger: custom}).logger())
	})
	t.Run("logger_default", func(t *testing.T) {
		require.NotNil(t, (&Loop{}).logger())
	})
}

// TestLoop_RenewIntervalFor covers the three cadence branches: explicit override,
// token-TTL/divisor derivation, and the zero-TTL fallback to the default floor.
func TestLoop_RenewIntervalFor(t *testing.T) {
	t.Run("explicit_override", func(t *testing.T) {
		l := &Loop{RenewInterval: gettersRenewInterval}
		require.Equal(t, gettersRenewInterval, l.renewIntervalFor(LeaseToken{}))
	})
	t.Run("derived_from_token_ttl", func(t *testing.T) {
		now := time.Now()
		tok := LeaseToken{AcquiredAt: now, ExpiresAt: now.Add(gettersTokenSpan)}
		require.Equal(t, gettersTokenSpan/renewIntervalDivisor, (&Loop{}).renewIntervalFor(tok))
	})
	t.Run("zero_ttl_falls_back_to_default", func(t *testing.T) {
		require.Equal(t, defaultRenewInterval, (&Loop{}).renewIntervalFor(LeaseToken{}))
	})
}

// TestLoop_CurrentEpoch_NoLease covers the no-live-lease branch (single-process /
// follower between terms) returning Epoch 0. The held-lease branch is covered by
// the leader-election integration tests.
func TestLoop_CurrentEpoch_NoLease(t *testing.T) {
	require.Equal(t, uint64(0), (&Loop{}).currentEpoch())
}
