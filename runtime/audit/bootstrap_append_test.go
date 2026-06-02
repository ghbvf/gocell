package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// testHMACKey is a fixed 32-byte HMAC key used by ledger.Protocol in tests.
// Length 32 is required by ledger.WithChainHMAC validation.
var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!")

// testNow is the deterministic timestamp returned by the fake clock so the
// Entry.Timestamp assertion is exact (UTC, no zone drift).
var testNow = time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC)

func buildTestLedgerStore(t *testing.T) (*audit.BootstrapLedgerStore, ledger.Store, clock.Clock) {
	t.Helper()
	p, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		append([]byte(nil), testHMACKey...),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "protocol construction")
	clk := clockmock.New(testNow)
	mem, err := ledger.NewMemStore(p, clk)
	require.NoError(t, err, "memstore construction")
	wrapped, err := audit.NewBootstrapLedgerStore(mem)
	require.NoError(t, err, "wrap bootstrap ledger store")
	return wrapped, mem, clk
}

// TestAppendBootstrapAuthFail_WritesEntryWithReasonAndClientIP covers T1.
// All three valid reasons × (with-IP, without-IP) write a well-formed entry
// to the ledger; payload is canonical JSON {"reason","clientIp"} camelCase.
func TestAppendBootstrapAuthFail_WritesEntryWithReasonAndClientIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		reason   string
		clientIP string
	}{
		{"missing_header_no_ip", "missing_header", ""},
		{"missing_header_with_ip", "missing_header", "192.0.2.1"},
		{"wrong_credentials_no_ip", "wrong_credentials", ""},
		{"wrong_credentials_with_ip", "wrong_credentials", "203.0.113.7"},
		{"rate_limited_no_ip", "rate_limited", ""},
		{"rate_limited_with_ip", "rate_limited", "198.51.100.42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, raw, clk := buildTestLedgerStore(t)

			err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, uuid.NewString(), tc.reason, tc.clientIP)
			require.NoError(t, err, "AppendBootstrapAuthFail must succeed for valid reason %q", tc.reason)

			entries, err := raw.Query(context.Background(),
				ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
				query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
			require.NoError(t, err, "ledger query")
			require.Len(t, entries, 1, "exactly one bootstrap.auth.fail entry expected")

			e := entries[0]
			assert.Equal(t, "bootstrap.auth.fail", e.EventType)
			assert.Equal(t, "system:bootstrap", e.ActorID)
			assert.NotEmpty(t, e.EventID, "EventID must be a non-empty UUID for HMAC fingerprint")
			assert.True(t, e.Timestamp.Equal(testNow.UTC()),
				"Timestamp must come from injected clock in UTC; got=%s want=%s", e.Timestamp, testNow.UTC())

			var got struct {
				Reason   string `json:"reason"`
				ClientIP string `json:"clientIp"`
			}
			require.NoError(t, json.Unmarshal(e.Payload, &got), "payload must be valid JSON")
			assert.Equal(t, tc.reason, got.Reason)
			assert.Equal(t, tc.clientIP, got.ClientIP,
				"clientIp must be passed through verbatim (empty when context carries none)")
		})
	}
}

// TestAppendBootstrapAuthFail_RejectsUnknownReason covers T2.
// Reason must be in the runtime/auth-mirrored whitelist; an unknown value
// returns ErrValidationFailed and never persists anything.
func TestAppendBootstrapAuthFail_RejectsUnknownReason(t *testing.T) {
	t.Parallel()
	cases := []string{"", "unknown_reason", "MISSING_HEADER", " rate_limited"}
	for _, reason := range cases {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			store, _, clk := buildTestLedgerStore(t)
			err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, uuid.NewString(), reason, "192.0.2.1")
			require.Error(t, err, "unknown reason %q must be rejected", reason)
			var coded *errcode.Error
			require.True(t, errors.As(err, &coded), "error must be *errcode.Error; got %T", err)
			assert.Equal(t, errcode.ErrValidationFailed, coded.Code,
				"reason whitelist failure must surface as ErrValidationFailed")

			snap, err := store.Tail(context.Background())
			require.NoError(t, err)
			assert.Equal(t, int64(0), snap.EntryCount, "no entry must be persisted on validation failure")
		})
	}
}

// TestAppendBootstrapAuthFail_RedeliverySameEventID_Deduplicates covers T4 (C1/F1).
// IdempotencyContentFingerprint keys on EventID alone. AppendBootstrapAuthFail
// now takes the stable source-event ID (outbox.Entry.ID()) as EventID, so
// at-least-once redelivery — the SAME eventID twice — collapses to
// ErrAuditLedgerAlreadyExists on the second call and the ledger holds exactly
// ONE entry. Two DIFFERENT events (distinct eventIDs) each persist.
// Regression guard: if the implementation ever reverts to minting a per-call
// uuid.NewString(), the redelivery case below would store two rows and fail.
func TestAppendBootstrapAuthFail_RedeliverySameEventID_Deduplicates(t *testing.T) {
	t.Parallel()
	store, raw, clk := buildTestLedgerStore(t)
	ctx := context.Background()
	eventID := uuid.NewString()

	// First delivery — must succeed.
	err := audit.AppendBootstrapAuthFail(ctx, store, clk, eventID, "rate_limited", "192.0.2.1")
	require.NoError(t, err, "first delivery must succeed")

	// Redelivery — same stable eventID ⇒ same fingerprint ⇒ idempotent dedup.
	err = audit.AppendBootstrapAuthFail(ctx, store, clk, eventID, "rate_limited", "192.0.2.1")
	require.Error(t, err, "redelivery of the same eventID must be rejected as duplicate")
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded), "duplicate error must be *errcode.Error; got %T", err)
	assert.Equal(t, errcode.ErrAuditLedgerAlreadyExists, coded.Code,
		"redelivery must surface ErrAuditLedgerAlreadyExists (idempotent replay)")

	entries, qerr := raw.Query(ctx,
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	require.NoError(t, qerr)
	assert.Len(t, entries, 1, "redelivery of the same eventID must leave exactly one ledger entry")

	// A genuinely distinct event (different eventID) persists independently.
	err = audit.AppendBootstrapAuthFail(ctx, store, clk, uuid.NewString(), "rate_limited", "192.0.2.1")
	require.NoError(t, err, "distinct eventID must persist a second entry")
	entries, qerr = raw.Query(ctx,
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	require.NoError(t, qerr)
	assert.Len(t, entries, 2, "two distinct eventIDs produce two ledger entries")
}

// TestAppendBootstrapAuthFail_EmptyEventID_Rejected covers the new eventID
// fail-fast guard (C1/F1): an empty idempotency key is rejected before any
// write, so it cannot silently collide all empty-id appends into one row.
func TestAppendBootstrapAuthFail_EmptyEventID_Rejected(t *testing.T) {
	t.Parallel()
	store, _, clk := buildTestLedgerStore(t)
	err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, "", "rate_limited", "192.0.2.1")
	require.Error(t, err, "empty eventID must be rejected")
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded), "error must be *errcode.Error; got %T", err)
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code,
		"empty eventID must surface as ErrValidationFailed")
}

// TestAppendBootstrapAuthFail_NilStoreOrClock_Errors covers T3.
// Both dependencies are strong-wired; bare-nil and typed-nil both reject.
func TestAppendBootstrapAuthFail_NilStoreOrClock_Errors(t *testing.T) {
	t.Parallel()
	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		_, _, clk := buildTestLedgerStore(t)
		err := audit.AppendBootstrapAuthFail(context.Background(), nil, clk, uuid.NewString(), "rate_limited", "192.0.2.1")
		require.Error(t, err)
		var coded *errcode.Error
		require.True(t, errors.As(err, &coded))
		assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
	})
	t.Run("nil clock", func(t *testing.T) {
		t.Parallel()
		store, _, _ := buildTestLedgerStore(t)
		err := audit.AppendBootstrapAuthFail(context.Background(), store, nil, uuid.NewString(), "missing_header", "")
		require.Error(t, err, "nil clock must be rejected; Timestamp provenance is load-bearing")
	})
}
