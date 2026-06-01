package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// =============================================================================
// decodeClaim — error branch coverage
// =============================================================================

// TestDecodeClaim_NotASlice covers the "unexpected result type" branch:
// decodeClaim receives a bare non-slice value (e.g. a string) instead of []any.
func TestDecodeClaim_NotASlice(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	state, rec, receipt, err := s.decodeClaim("notaslice", "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "unexpected result type")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestDecodeClaim_EmptySlice covers the "unexpected result type" branch via
// empty []any (len == 0 guard).
func TestDecodeClaim_EmptySlice(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	state, rec, receipt, err := s.decodeClaim([]any{}, "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "unexpected result type")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestDecodeClaim_CodeNotInt64 covers the "reply code not an integer" branch:
// the leading element of the slice is not an int64.
func TestDecodeClaim_CodeNotInt64(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	state, rec, receipt, err := s.decodeClaim([]any{"notanint"}, "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "reply code not an integer")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestDecodeClaim_UnknownCode covers the "unexpected result code" default branch
// for a code value other than 0, 1, or 2.
func TestDecodeClaim_UnknownCode(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	state, rec, receipt, err := s.decodeClaim([]any{int64(9)}, "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "unexpected result code")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// =============================================================================
// decodeClaimDone — error branch coverage
// =============================================================================

// TestDecodeClaimDone_MissingBlob covers the "done reply missing response blob"
// branch: code=2 but the array has only one element.
func TestDecodeClaimDone_MissingBlob(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	// arr[0] == int64(2) but no second element
	state, rec, receipt, err := s.decodeClaim([]any{int64(2)}, "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "done reply missing response blob")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestDecodeClaimDone_BlobNotString covers the "done reply blob not a string" branch.
func TestDecodeClaimDone_BlobNotString(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	state, rec, receipt, err := s.decodeClaim([]any{int64(2), int64(999)}, "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "blob not a string")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestDecodeClaimDone_UndecodableBlob covers the "claim replay decode failed"
// branch: code=2 with a well-typed but invalid JSON blob.
func TestDecodeClaimDone_UndecodableBlob(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())

	state, rec, receipt, err := s.decodeClaim([]any{int64(2), "not-valid-json"}, "rk", "lk", "fpk", "tok")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisGet, ec.Code)
	assert.Contains(t, err.Error(), "replay decode failed")
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// =============================================================================
// Claim — input validation error branches
// =============================================================================

// TestHTTPIdempotencyStore_Claim_EmptyNS covers the "ns must be non-empty" branch.
func TestHTTPIdempotencyStore_Claim_EmptyNS(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())
	ctx := context.Background()

	state, rec, receipt, err := s.Claim(ctx, "", "somekey", "", idempotency.DefaultLeaseTTL)

	require.Error(t, err)
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
}

// TestHTTPIdempotencyStore_Claim_NSWithBraces covers the "ns contains curly braces" branch.
func TestHTTPIdempotencyStore_Claim_NSWithBraces(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())
	ctx := context.Background()

	state, rec, receipt, err := s.Claim(ctx, "bad{ns}", "somekey", "", idempotency.DefaultLeaseTTL)

	require.Error(t, err)
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestHTTPIdempotencyStore_Claim_EmptyKey covers the "key must be non-empty" branch.
func TestHTTPIdempotencyStore_Claim_EmptyKey(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())
	ctx := context.Background()

	state, rec, receipt, err := s.Claim(ctx, "goodns", "", "", idempotency.DefaultLeaseTTL)

	require.Error(t, err)
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
}

// TestHTTPIdempotencyStore_Claim_KeyWithBraces covers the "key contains curly braces" branch.
func TestHTTPIdempotencyStore_Claim_KeyWithBraces(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())
	ctx := context.Background()

	state, rec, receipt, err := s.Claim(ctx, "goodns", "bad{key}", "", idempotency.DefaultLeaseTTL)

	require.Error(t, err)
	assert.Zero(t, state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// TestHTTPIdempotencyStore_Claim_ZeroLeaseTTLClamped verifies that a zero/negative
// leaseTTL is clamped to DefaultLeaseTTL (the claim still succeeds).
func TestHTTPIdempotencyStore_Claim_ZeroLeaseTTLClamped(t *testing.T) {
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, newHTTPClaimerMock())
	ctx := context.Background()

	state, _, _, err := s.Claim(ctx, "testns", "key:ttlclamp", "", 0)

	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
}

// =============================================================================
// noopHTTPReceipt — Record and Release return ErrNoClaimLease
// =============================================================================

// TestNoopHTTPReceipt_RecordReturnsErrNoClaimLease verifies that the noop
// receipt returned for ClaimBusy/ClaimDone paths returns ErrNoClaimLease
// from Record.
func TestNoopHTTPReceipt_RecordReturnsErrNoClaimLease(t *testing.T) {
	// Obtain a noopHTTPReceipt via ClaimBusy path.
	mock := newHTTPClaimerMock()
	ctx := context.Background()

	// Pre-set the lease to simulate another consumer holding it.
	mock.mu.Lock()
	mock.store["testns:{key:noop:001}:lease"] = mockEntry{
		value: "other-token",
	}
	mock.mu.Unlock()

	s := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	state, _, receipt, err := s.Claim(ctx, "testns", "key:noop:001", "", idempotency.DefaultLeaseTTL)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimBusy, state)
	require.NotNil(t, receipt)

	// Build a valid RecordedResponse to pass to Record.
	raw := []byte(`{"status":200,"body":"dGVzdA==","header":{},"recordedAt":"2024-01-01T00:00:00Z"}`)
	rec, unmarshalErr := idemhttp.UnmarshalRecordedResponse(raw)
	require.NoError(t, unmarshalErr)

	err = receipt.Record(ctx, &rec, idempotency.DefaultTTL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, idempotency.ErrNoClaimLease),
		"noopHTTPReceipt.Record must return ErrNoClaimLease, got: %v", err)
}

// TestNoopHTTPReceipt_ReleaseReturnsErrNoClaimLease verifies that the noop
// receipt returned for ClaimBusy/ClaimDone paths returns ErrNoClaimLease
// from Release.
func TestNoopHTTPReceipt_ReleaseReturnsErrNoClaimLease(t *testing.T) {
	mock := newHTTPClaimerMock()
	ctx := context.Background()

	// Pre-set the lease to simulate another consumer.
	mock.mu.Lock()
	mock.store["testns:{key:noop:002}:lease"] = mockEntry{
		value: "other-token",
	}
	mock.mu.Unlock()

	s := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	state, _, receipt, err := s.Claim(ctx, "testns", "key:noop:002", "", idempotency.DefaultLeaseTTL)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimBusy, state)
	require.NotNil(t, receipt)

	err = receipt.Release(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, idempotency.ErrNoClaimLease),
		"noopHTTPReceipt.Release must return ErrNoClaimLease, got: %v", err)
}

// TestNoopHTTPReceipt_ViaClaimDone_RecordAndRelease verifies both noopHTTPReceipt
// methods via the ClaimDone path, which also returns a noopHTTPReceipt.
func TestNoopHTTPReceipt_ViaClaimDone_RecordAndRelease(t *testing.T) {
	mock := newHTTPClaimerMock()
	ctx := context.Background()
	s := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)

	// First: acquire and record a response so the second claim is ClaimDone.
	_, _, firstReceipt, err := s.Claim(ctx, "testns", "key:noop:003", "", idempotency.DefaultLeaseTTL)
	require.NoError(t, err)

	raw := []byte(`{"status":200,"body":"dGVzdA==","header":{},"recordedAt":"2024-01-01T00:00:00Z"}`)
	rec, unmarshalErr := idemhttp.UnmarshalRecordedResponse(raw)
	require.NoError(t, unmarshalErr)
	require.NoError(t, firstReceipt.Record(ctx, &rec, idempotency.DefaultTTL))

	// Second claim returns ClaimDone with a noopHTTPReceipt.
	state, _, noopR, err2 := s.Claim(ctx, "testns", "key:noop:003", "", idempotency.DefaultLeaseTTL)
	require.NoError(t, err2)
	require.Equal(t, idempotency.ClaimDone, state)
	require.NotNil(t, noopR)

	assert.True(t, errors.Is(noopR.Record(ctx, &rec, idempotency.DefaultTTL), idempotency.ErrNoClaimLease))
	assert.True(t, errors.Is(noopR.Release(ctx), idempotency.ErrNoClaimLease))
}
