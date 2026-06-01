//go:build integration

package redis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// TODO(#1043): switch to idempotencytest.RunConformanceSuite once Batch 3 lands.
// Expected signature:
//
//	func RunConformanceSuite(t *testing.T, factory func(t *testing.T) (idemhttp.Store, *clockmock.FakeClock, func()))
//
// Until then, run a direct integration test against a real Redis instance.

// TestIntegration_HTTPIdempotencyStore verifies the dual-key Lua model against
// a real Redis instance via testcontainers.
//
// Cases covered:
//   - Claim → ClaimAcquired
//   - Record (commit) → ClaimDone replay
//   - Concurrent second claim → ClaimBusy
//   - Release → re-claim succeeds
func TestIntegration_HTTPIdempotencyStore(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	store, err := NewHTTPIdempotencyStore(client, testNamespace)
	require.NoError(t, err)

	const claimNS = "integ-http-idem"
	const key = "test-request-001"

	// ── ClaimAcquired ─────────────────────────────────────────────────────────
	t.Run("ClaimAcquired", func(t *testing.T) {
		state, rec, receipt, err := store.Claim(ctx, claimNS, key, testtime.D5min)
		require.NoError(t, err)
		assert.Equal(t, idempotency.ClaimAcquired, state)
		assert.Nil(t, rec)
		assert.NotNil(t, receipt)

		// ── ClaimBusy while lease held ─────────────────────────────────────────
		t.Run("ClaimBusy", func(t *testing.T) {
			state2, rec2, _, err2 := store.Claim(ctx, claimNS, key, testtime.D5min)
			require.NoError(t, err2)
			assert.Equal(t, idempotency.ClaimBusy, state2)
			assert.Nil(t, rec2)
		})

		// ── Record and replay ─────────────────────────────────────────────────
		t.Run("RecordAndReplay", func(t *testing.T) {
			raw := []byte(`{"status":200,"body":"aGVsbG8=","header":{},"recordedAt":"2024-06-01T12:00:00Z"}`)
			resp, err := idemhttp.UnmarshalRecordedResponse(raw)
			require.NoError(t, err)

			err = receipt.Record(ctx, &resp, testtime.SelectAsyncSettle)
			require.NoError(t, err)

			// Second claim should return ClaimDone with the stored response.
			state3, rec3, _, err3 := store.Claim(ctx, claimNS, key, testtime.D5min)
			require.NoError(t, err3)
			assert.Equal(t, idempotency.ClaimDone, state3)
			require.NotNil(t, rec3)
			assert.Equal(t, 200, rec3.Status())
		})
	})
}

// TestIntegration_HTTPIdempotencyStore_Release verifies that Release removes
// the lease and allows a subsequent Claim to re-acquire.
func TestIntegration_HTTPIdempotencyStore_Release(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	store, err := NewHTTPIdempotencyStore(client, testNamespace)
	require.NoError(t, err)

	const claimNS = "integ-http-idem-rel"
	key := "release-test-" + t.Name()

	state, _, receipt, err := store.Claim(ctx, claimNS, key, testtime.D5min)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)

	err = receipt.Release(ctx)
	require.NoError(t, err)

	// After Release, re-claim should succeed.
	state2, _, _, err2 := store.Claim(ctx, claimNS, key, testtime.D5min)
	require.NoError(t, err2)
	assert.Equal(t, idempotency.ClaimAcquired, state2)
}
