package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
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

// TestAppendBootstrapAuthFail_WritesEntryWithReasonAndClientIPHash covers T1.
// All three valid reasons × (with-hash, without-hash) write a well-formed entry
// to the ledger; payload is canonical JSON {"reason","clientIpHash"} camelCase.
// The composition root has already hashed the IP (#1488), so this layer stores
// the opaque hash string verbatim — never a plaintext IP.
func TestAppendBootstrapAuthFail_WritesEntryWithReasonAndClientIPHash(t *testing.T) {
	t.Parallel()
	// Valid clientIpHash is empty or a 64-char lowercase-hex HMAC-SHA256 digest
	// (redaction.IsIPHashString); short/old fixtures would now be rejected.
	h64a := strings.Repeat("a1b2c3d4", 8)
	h64b := strings.Repeat("deadbeef", 8)
	h64c := strings.Repeat("01234567", 8)
	tests := []struct {
		name         string
		reason       string
		clientIPHash string
	}{
		{"missing_header_no_hash", "missing_header", ""},
		{"missing_header_with_hash", "missing_header", h64a},
		{"wrong_credentials_no_hash", "wrong_credentials", ""},
		{"wrong_credentials_with_hash", "wrong_credentials", h64b},
		{"rate_limited_no_hash", "rate_limited", ""},
		{"rate_limited_with_hash", "rate_limited", h64c},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, raw, clk := buildTestLedgerStore(t)

			err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, uuid.NewString(), tc.reason, tc.clientIPHash)
			require.NoError(t, err, "AppendBootstrapAuthFail must succeed for valid reason %q", tc.reason)

			rawVis, _ := tenant.NewRowVisibility(tenant.RowScopeTenant, "")
			entries, err := raw.Query(context.Background(), tenant.TenantID(""), rawVis,
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
				Reason       string `json:"reason"`
				ClientIPHash string `json:"clientIpHash"`
			}
			require.NoError(t, json.Unmarshal(e.Payload, &got), "payload must be valid JSON")
			assert.Equal(t, tc.reason, got.Reason)
			assert.Equal(t, tc.clientIPHash, got.ClientIPHash,
				"clientIpHash must be passed through verbatim (empty when no IP)")
		})
	}
}

// TestAppendBootstrapAuthFail_RejectsMalformedClientIPHash is the consumer
// trust-boundary guard (#1488 F1): an untrusted wire event whose clientIpHash is
// neither empty nor a 64-hex digest (plaintext-laundering, short/old hash,
// uppercase, non-hex) must be rejected with ErrValidationFailed and never reach
// the ledger — the sealed IPHash producer type does not cover the consumer.
func TestAppendBootstrapAuthFail_RejectsMalformedClientIPHash(t *testing.T) {
	t.Parallel()
	store, _, clk := buildTestLedgerStore(t)
	for _, bad := range []string{
		"192.0.2.1",                   // plaintext IP laundered as the hash field
		"a1b2c3d4",                    // too short (not 64 chars)
		strings.Repeat("A1B2C3D4", 8), // uppercase hex (HashIP emits lowercase)
		strings.Repeat("z", 64),       // 64 chars but non-hex
	} {
		err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, uuid.NewString(), "missing_header", bad)
		require.Error(t, err, "malformed clientIpHash %q must be rejected", bad)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
			"malformed clientIpHash must be a permanent ErrValidationFailed (→ DLX)")
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
	h64 := strings.Repeat("ab", 32) // valid 64-hex clientIpHash

	// First delivery — must succeed.
	err := audit.AppendBootstrapAuthFail(ctx, store, clk, eventID, "rate_limited", h64)
	require.NoError(t, err, "first delivery must succeed")

	// Redelivery — same stable eventID ⇒ same fingerprint ⇒ idempotent dedup.
	err = audit.AppendBootstrapAuthFail(ctx, store, clk, eventID, "rate_limited", h64)
	require.Error(t, err, "redelivery of the same eventID must be rejected as duplicate")
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded), "duplicate error must be *errcode.Error; got %T", err)
	assert.Equal(t, errcode.ErrAuditLedgerAlreadyExists, coded.Code,
		"redelivery must surface ErrAuditLedgerAlreadyExists (idempotent replay)")

	idempotVis, _ := tenant.NewRowVisibility(tenant.RowScopeTenant, "")
	entries, qerr := raw.Query(ctx, tenant.TenantID(""), idempotVis,
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	require.NoError(t, qerr)
	assert.Len(t, entries, 1, "redelivery of the same eventID must leave exactly one ledger entry")

	// A genuinely distinct event (different eventID) persists independently.
	err = audit.AppendBootstrapAuthFail(ctx, store, clk, uuid.NewString(), "rate_limited", h64)
	require.NoError(t, err, "distinct eventID must persist a second entry")
	entries, qerr = raw.Query(ctx, tenant.TenantID(""), idempotVis,
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
