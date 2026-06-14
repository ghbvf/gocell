package saga

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ksaga "github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// TestNewJournal_NilPool covers the nil-pool validation branch of NewJournal
// (the happy path is covered by every integration test, but the error branch
// is otherwise dead in test runs).
func TestNewJournal_NilPool(t *testing.T) {
	t.Parallel()
	j, err := NewJournal(nil, nil) // nil pool short-circuits before clock check
	require.Nil(t, j)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInvalid, ec.Kind)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// TestStatusToInt16_InvalidReturnsZero ensures invalid status values
// produce the 0 sentinel that the DB CHECK constraint
// (saga_instances_status_range) will reject — fail-closed mapping.
func TestStatusToInt16_InvalidReturnsZero(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int16(0), statusToInt16(ksaga.Status(0)),
		"zero (uninitialized) status maps to 0 sentinel")
	assert.Equal(t, int16(0), statusToInt16(ksaga.Status(99)),
		"out-of-range status maps to 0 sentinel")
	assert.Equal(t, int16(1), statusToInt16(ksaga.StatusPending),
		"valid status round-trips its iota+1 value")
	assert.Equal(t, int16(7), statusToInt16(ksaga.StatusExpired),
		"max valid status (Expired=7) round-trips")
}

// TestStatusFromInt16_RangeAndValidGates covers both gates: the int8 range
// check (rejects negative / overflow) and the saga.Status.Valid() check
// (rejects out-of-enum values in the int8 range).
func TestStatusFromInt16_RangeAndValidGates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      int16
		want    ksaga.Status
		wantErr string
	}{
		{"valid_pending", 1, ksaga.StatusPending, ""},
		{"valid_expired", 7, ksaga.StatusExpired, ""},
		{"zero_invalid", 0, 0, "out of range"},
		{"negative_out_of_int8_range", -1, 0, "out of uint8 range"},
		{"overflow_out_of_int8_range", 256, 0, "out of uint8 range"},
		{"in_int8_range_but_unknown_enum", 99, 0, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := statusFromInt16(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestKindToInt16_InvalidReturnsZero mirrors statusToInt16 for EventKind.
func TestKindToInt16_InvalidReturnsZero(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int16(0), kindToInt16(journal.EventKind(0)))
	assert.Equal(t, int16(0), kindToInt16(journal.EventKind(99)))
	assert.Equal(t, int16(1), kindToInt16(journal.KindStepStarted))
	assert.Equal(t, int16(9), kindToInt16(journal.KindSagaExpired))
}

// TestKindFromInt16_RangeAndValidGates mirrors statusFromInt16 for EventKind.
func TestKindFromInt16_RangeAndValidGates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      int16
		want    journal.EventKind
		wantErr string
	}{
		{"valid_step_started", 1, journal.KindStepStarted, ""},
		{"valid_saga_expired", 9, journal.KindSagaExpired, ""},
		{"zero_invalid", 0, 0, "out of range"},
		{"negative_out_of_int8_range", -5, 0, "out of uint8 range"},
		{"overflow_out_of_int8_range", 1000, 0, "out of uint8 range"},
		{"in_int8_range_but_unknown_enum", 99, 0, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := kindFromInt16(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestFenced exercises the three gating branches plus the exact-equality
// boundary that aligns with memjournal's `!Before(now)` semantic.
func TestFenced(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	lease := idutil.SafeID("lease-1")
	cases := []struct {
		name string
		row  instanceRow
		id   idutil.SafeID
		want bool
	}{
		{
			name: "no_lease_set",
			row:  instanceRow{leaseValid: false},
			id:   lease,
			want: false,
		},
		{
			name: "lease_id_mismatch",
			row:  instanceRow{leaseValid: true, leaseID: "other-lease", leaseExpiresAt: now.Add(time.Hour)},
			id:   lease,
			want: false,
		},
		{
			name: "lease_expired_strictly_before_now",
			row:  instanceRow{leaseValid: true, leaseID: string(lease), leaseExpiresAt: now.Add(-time.Second)},
			id:   lease,
			want: false,
		},
		{
			name: "lease_valid_in_future",
			row:  instanceRow{leaseValid: true, leaseID: string(lease), leaseExpiresAt: now.Add(time.Hour)},
			id:   lease,
			want: true,
		},
		{
			name: "lease_valid_at_exact_equality_boundary",
			row:  instanceRow{leaseValid: true, leaseID: string(lease), leaseExpiresAt: now},
			id:   lease,
			want: true, // memjournal !Before(now) parity
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, fenced(tc.row, tc.id, now))
		})
	}
}

// TestNullableStepName covers the empty/non-empty branches of the helper
// that maps StepName to PG NULL for saga-scoped events.
func TestNullableStepName(t *testing.T) {
	t.Parallel()
	assert.Nil(t, nullableStepName(idutil.SafeID("")),
		"empty StepName must map to nil (PG NULL)")
	assert.Equal(t, "step-a", nullableStepName(idutil.SafeID("step-a")),
		"non-empty StepName must round-trip as string")
}
