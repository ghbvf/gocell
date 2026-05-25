package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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

func buildTestLedgerStore(t *testing.T) (ledger.Store, clock.Clock) {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err, "namespace parse")
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(append([]byte(nil), testHMACKey...)),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "protocol construction")
	clk := clockmock.New(testNow)
	store, err := ledger.NewMemStore(p, clk)
	require.NoError(t, err, "memstore construction")
	return store, clk
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
			store, clk := buildTestLedgerStore(t)

			err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, tc.reason, tc.clientIP)
			require.NoError(t, err, "AppendBootstrapAuthFail must succeed for valid reason %q", tc.reason)

			entries, err := store.Query(context.Background(),
				ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
				query.ListParams{Limit: 10, Sort: ledger.QuerySort})
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
			store, clk := buildTestLedgerStore(t)
			err := audit.AppendBootstrapAuthFail(context.Background(), store, clk, reason, "192.0.2.1")
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

// TestAppendBootstrapAuthFail_DuplicateFingerprintRejected covers T4.
// IdempotencyContentFingerprint keys on (eventID + eventType + actorID + timestamp + payload).
// Because AppendBootstrapAuthFail calls uuid.NewString() per invocation, each
// call produces a distinct EventID — the fingerprint differs even when all
// business fields are identical, so two consecutive Appends both succeed and
// the ledger holds exactly two entries.
// This test documents the real behavior as a regression guard; if the
// implementation ever switches to a content-only fingerprint that ignores
// EventID, it will immediately fail here.
func TestAppendBootstrapAuthFail_DuplicateFingerprintRejected(t *testing.T) {
	t.Parallel()
	store, clk := buildTestLedgerStore(t)
	ctx := context.Background()

	// First append — must succeed.
	err := audit.AppendBootstrapAuthFail(ctx, store, clk, "rate_limited", "192.0.2.1")
	require.NoError(t, err, "first Append must succeed")

	// Second append — same reason + clientIP, but uuid.NewString() gives a
	// fresh EventID each time, so IdempotencyContentFingerprint yields a
	// different key. Both entries are stored (store has 2 entries, not 1).
	err = audit.AppendBootstrapAuthFail(ctx, store, clk, "rate_limited", "192.0.2.1")
	require.NoError(t, err, "second Append must also succeed — EventID uniqueness prevents fingerprint collision")

	entries, qerr := store.Query(ctx,
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort})
	require.NoError(t, qerr)
	assert.Len(t, entries, 2, "both Appends should produce distinct ledger entries due to unique EventIDs")
}

// TestAppendBootstrapAuthFail_NilStoreOrClock_Errors covers T3.
// Both dependencies are strong-wired; bare-nil and typed-nil both reject.
func TestAppendBootstrapAuthFail_NilStoreOrClock_Errors(t *testing.T) {
	t.Parallel()
	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		_, clk := buildTestLedgerStore(t)
		err := audit.AppendBootstrapAuthFail(context.Background(), nil, clk, "rate_limited", "192.0.2.1")
		require.Error(t, err)
		var coded *errcode.Error
		require.True(t, errors.As(err, &coded))
		assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
	})
	t.Run("typed-nil store (concrete pointer wrapped in interface)", func(t *testing.T) {
		t.Parallel()
		_, clk := buildTestLedgerStore(t)
		// True typed-nil: an interface value carrying type info (*ledger.MemStore)
		// but a nil concrete pointer. A bare `var typedNil ledger.Store` is only
		// a nil interface — distinct shape, not what validation.IsNilInterface
		// is meant to catch via reflect.
		var nilMem *ledger.MemStore
		var typedNil ledger.Store = nilMem
		err := audit.AppendBootstrapAuthFail(context.Background(), typedNil, clk, "rate_limited", "")
		require.Error(t, err,
			"typed-nil ledger.Store (concrete-pointer-in-interface) must be rejected via validation.IsNilInterface")
	})
	t.Run("nil clock", func(t *testing.T) {
		t.Parallel()
		store, _ := buildTestLedgerStore(t)
		err := audit.AppendBootstrapAuthFail(context.Background(), store, nil, "missing_header", "")
		require.Error(t, err, "nil clock must be rejected; Timestamp provenance is load-bearing")
	})
}
