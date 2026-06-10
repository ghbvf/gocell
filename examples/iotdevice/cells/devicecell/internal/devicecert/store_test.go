package devicecert

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var certTestBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

func TestStore_Issue_FirstEpochIsOne(t *testing.T) {
	t.Parallel()
	s := NewStore()

	st, err := s.Issue(context.Background(), "dev-1", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, "dev-1", st.DeviceID)
	assert.Equal(t, int64(1), st.Epoch, "first Issue must start epoch at 1")
	assert.Equal(t, certTestBase.Add(24*time.Hour), st.NotAfter)
}

func TestStore_Issue_ReissueAdvancesEpoch(t *testing.T) {
	t.Parallel()
	s := NewStore()
	ctx := context.Background()

	_, err := s.Issue(ctx, "dev-1", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)
	st2, err := s.Issue(ctx, "dev-1", certTestBase.Add(72*time.Hour))
	require.NoError(t, err)

	assert.Equal(t, int64(2), st2.Epoch, "re-issuing a cert must advance the epoch")
	assert.Equal(t, certTestBase.Add(72*time.Hour), st2.NotAfter, "re-issue must update NotAfter")
}

func TestStore_Issue_Validation(t *testing.T) {
	t.Parallel()
	s := NewStore()
	ctx := context.Background()

	_, err := s.Issue(ctx, "", certTestBase.Add(time.Hour))
	require.Error(t, err, "empty deviceID must fail-fast")

	_, err = s.Issue(ctx, "dev-1", time.Time{})
	require.Error(t, err, "zero notAfter must fail-fast")
}

func TestStore_ScanNearExpiry(t *testing.T) {
	t.Parallel()
	s := NewStore()
	ctx := context.Background()
	// Seed three devices with distinct expiries.
	require.NoErrorf(t, mustIssue(s, "dev-a", certTestBase.Add(1*time.Hour)), "seed dev-a")
	require.NoErrorf(t, mustIssue(s, "dev-c", certTestBase.Add(10*time.Hour)), "seed dev-c")
	require.NoErrorf(t, mustIssue(s, "dev-b", certTestBase.Add(5*time.Hour)), "seed dev-b")

	tests := []struct {
		name   string
		cutoff time.Time
		want   []string // expected DeviceIDs, sorted
	}{
		{"none near expiry", certTestBase, nil},
		{"boundary inclusive", certTestBase.Add(1 * time.Hour), []string{"dev-a"}},
		{"two near expiry", certTestBase.Add(5 * time.Hour), []string{"dev-a", "dev-b"}},
		{"all near expiry", certTestBase.Add(24 * time.Hour), []string{"dev-a", "dev-b", "dev-c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ScanNearExpiry(ctx, tc.cutoff)
			require.NoError(t, err)
			var ids []string
			for _, st := range got {
				ids = append(ids, st.DeviceID)
			}
			assert.Equal(t, tc.want, ids, "ScanNearExpiry must return NotAfter<=cutoff sorted by DeviceID")
		})
	}
}

func TestStore_ScanNearExpiry_Empty(t *testing.T) {
	t.Parallel()
	s := NewStore()
	got, err := s.ScanNearExpiry(context.Background(), certTestBase.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestStore_MarkRenewalRequested_SkipsScannedEpoch is the F1 core: once an epoch
// is marked renewal-requested, ScanNearExpiry stops returning it — the per-epoch
// single-emit guarantee that no longer leans on the relay's 24h command-done TTL.
func TestStore_MarkRenewalRequested_SkipsScannedEpoch(t *testing.T) {
	t.Parallel()
	s := NewStore()
	ctx := context.Background()
	cutoff := certTestBase.Add(24 * time.Hour)
	require.NoError(t, mustIssue(s, "dev-1", certTestBase.Add(time.Hour)))

	got, err := s.ScanNearExpiry(ctx, cutoff)
	require.NoError(t, err)
	require.Len(t, got, 1, "near-expiry cert is scannable before it is requested")

	require.NoError(t, s.MarkRenewalRequested(ctx, "dev-1", 1))

	got, err = s.ScanNearExpiry(ctx, cutoff)
	require.NoError(t, err)
	assert.Empty(t, got, "a renewal-requested epoch is skipped on subsequent scans")
}

// TestStore_MarkRenewalRequested_StaleEpochNoop guards the compare-and-set: a mark
// for an epoch that no longer matches the stored cert (re-issued since the scan)
// must NOT suppress the newer epoch.
func TestStore_MarkRenewalRequested_StaleEpochNoop(t *testing.T) {
	t.Parallel()
	s := NewStore()
	ctx := context.Background()
	cutoff := certTestBase.Add(24 * time.Hour)
	require.NoError(t, mustIssue(s, "dev-1", certTestBase.Add(time.Hour)))

	// Marking a stale epoch (2 — the cert is still at epoch 1) is a no-op.
	require.NoError(t, s.MarkRenewalRequested(ctx, "dev-1", 2))
	got, err := s.ScanNearExpiry(ctx, cutoff)
	require.NoError(t, err)
	require.Len(t, got, 1, "marking a non-matching epoch must not suppress the live epoch")

	// Unknown device is a no-op, no error.
	require.NoError(t, s.MarkRenewalRequested(ctx, "dev-absent", 1))
}

// TestStore_Reissue_AfterRequest_IsScannableAgain proves the forcing function: a
// post-rotation re-issue advances the epoch, which is not yet renewal-requested,
// so the cert becomes dispatchable again.
func TestStore_Reissue_AfterRequest_IsScannableAgain(t *testing.T) {
	t.Parallel()
	s := NewStore()
	ctx := context.Background()
	cutoff := certTestBase.Add(24 * time.Hour)
	require.NoError(t, mustIssue(s, "dev-1", certTestBase.Add(time.Hour)))
	require.NoError(t, s.MarkRenewalRequested(ctx, "dev-1", 1))

	got, err := s.ScanNearExpiry(ctx, cutoff)
	require.NoError(t, err)
	require.Empty(t, got, "epoch 1 is suppressed after being requested")

	// Re-issue (still near-expiry) advances to epoch 2.
	st, err := s.Issue(ctx, "dev-1", certTestBase.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(2), st.Epoch)
	require.Zero(t, st.RenewalRequestedEpoch, "a fresh epoch is not renewal-requested")

	got, err = s.ScanNearExpiry(ctx, cutoff)
	require.NoError(t, err)
	require.Len(t, got, 1, "the new epoch is dispatchable again")
	assert.Equal(t, int64(2), got[0].Epoch)
}

func mustIssue(s *Store, id string, notAfter time.Time) error {
	_, err := s.Issue(context.Background(), id, notAfter)
	return err
}
