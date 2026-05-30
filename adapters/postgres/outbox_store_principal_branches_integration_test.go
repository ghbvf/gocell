//go:build integration

package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	rout "github.com/ghbvf/gocell/runtime/outbox"
)

// FP7 #1291 followup (#1309): PG-specific persistence-layer branch tests for the
// outbox metadata/observability/principal reconstruction and occurred_at
// boundaries.
//
// What is already covered elsewhere (so these tests do not duplicate it):
//   - FP2 (#1294) — happy-path round-trip via
//     outboxtest.RunPrincipalRoundTripConformance (store-agnostic).
//   - #1283 — outbox_store_decode_test.go unit-tests decodeOversizeGuardedJSONB
//     directly with raw bytes (empty/oversize/invalid-json/validate-fail/valid).
//
// The gap these tests close is the END-TO-END PG path the unit test cannot reach
// (its own comment notes the drop branches "only reach [the helper] through a
// live DB row"): a real corrupt/oversized JSONB column read back through
// ClaimPending must be dropped to its zero value, the entry still claimed, and a
// Warn emitted with the entry_id/event_type correlation fields. It additionally
// covers the metadata column (which is decoded INLINE, not through the generic
// helper, so the unit test does not touch it) and the occurred_at boundaries
// (timestamptz microsecond round-trip + the migration-044 NOT NULL constraint),
// neither of which is asserted anywhere else.
//
// No principal-validate case: PrincipalMetadata is all-SafeID, and
// SafeID.UnmarshalJSON enforces every field invariant (the same
// validateSafeIDString that Validate calls) at decode time, so the generic
// helper's Validate() branch is structurally unreachable for principal on the
// wire path. The branch itself is shared generic code exercised via the
// observability type (which has a plain-string traceParent with no UnmarshalJSON
// guard) in outbox_store_decode_test.go and in the observability_validate case
// below — so it is covered without a redundant, unreachable principal case.

const (
	// principalBranchOversizeLen produces a JSONB column larger than every
	// column's 4096-byte scan-side cap (maxMetadataJSONBytes / maxObservability
	// JSONBytes / maxPrincipalJSONBytes are all 4 KB), so the oversize guard
	// fires before the unmarshal/validate guards.
	principalBranchOversizeLen = 5000
	// invalidTraceParentJSON is a well-formed JSON object whose traceParent is not
	// a valid W3C traceparent. It unmarshals cleanly (traceParent is a plain
	// string with no UnmarshalJSON guard) but fails ObservabilityMetadata.Validate,
	// reaching the generic helper's validate branch — the only validate branch
	// reachable on the wire path.
	invalidTraceParentJSON = `{"traceParent":"not-a-valid-w3c-traceparent"}`
	// occurredAtBranchSkew offsets occurred_at (producer-domain event time) from
	// created_at (store/seal time) so the round-trip proves the two timestamps are
	// carried as independent columns. Extracted to a const per TEST-TIME-LITERAL-01.
	occurredAtBranchSkew = 90 * time.Minute
)

// installWarnCapture redirects the package-level slog default (which
// scanClaimedEntry's slog.Warn calls resolve through) to a JSON buffer for the
// duration of the test and restores it on cleanup. Tests using it MUST NOT call
// t.Parallel() — they mutate global logger state.
func installWarnCapture(t *testing.T) *sloghelper.SyncBuffer {
	t.Helper()
	buf := sloghelper.NewSyncBuffer()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// insertCorruptOutboxRow inserts a single pending outbox row with caller-supplied
// raw JSONB text for the metadata / observability / principal columns, so a test
// can plant a malformed or oversized column that insertSeedRow (which only
// marshals well-formed Entry values) cannot express. All base fields are valid so
// the row, after the corrupt column is dropped to zero, still produces a
// Validate-passing Entry (otherwise ToEntry would fail the whole claim batch).
func insertCorruptOutboxRow(t *testing.T, pool *Pool, id, metadataJSON, observabilityJSON, principalJSON string) {
	t.Helper()
	const insertSQL = `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload,
		 metadata, observability, principal, occurred_at, created_at, status, attempts)
		VALUES ($1, '', '', $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8, $9, 'pending', 0)`
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := pool.DB().Exec(context.Background(), insertSQL,
		id, "user.login", "user.login", []byte(`{"action":"login"}`),
		metadataJSON, observabilityJSON, principalJSON, now, now)
	require.NoError(t, err, "insertCorruptOutboxRow must succeed for id %s", id)
}

// bigJSONField returns `{"<key>":"<n×x>"}` for planting oversize values into a
// JSONB column.
func bigJSONField(key string, n int) string {
	return fmt.Sprintf(`{%q:%q}`, key, strings.Repeat("x", n))
}

// TestPGOutboxStore_PrincipalReconstruct_FailSoftBranches exercises every
// wire-reachable fail-soft drop+Warn branch in scanClaimedEntry through a real
// PG row: each plants a corrupt column, claims the row, and asserts (a) the
// targeted field is dropped to its zero value and (b) the branch-specific Warn
// line is emitted with the entry_id/event_type correlation fields.
//
// wantWarn values are substrings (FindLogEntry matches on substring) chosen to
// stay stable across the message punctuation (the messages end in "— dropping").
func TestPGOutboxStore_PrincipalReconstruct_FailSoftBranches(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	store := NewOutboxStore(pool.DB(), clock.Real())

	cases := []struct {
		name string
		// column injections (raw JSONB text); "{}" is the inert/valid default.
		metadata      string
		observability string
		principal     string
		wantWarn      string
		// assert verifies the targeted field was dropped to its zero value.
		assert func(t *testing.T, ce rout.ClaimedEntry)
	}{
		{
			name:     "metadata_oversize",
			metadata: bigJSONField("k", principalBranchOversizeLen), observability: "{}", principal: "{}",
			wantWarn: "metadata JSON exceeds max size",
			assert:   func(t *testing.T, ce rout.ClaimedEntry) { assert.Empty(t, ce.Metadata()) },
		},
		{
			name:     "metadata_unmarshal_failure",
			metadata: "[]", observability: "{}", principal: "{}",
			wantWarn: "failed to unmarshal metadata",
			assert:   func(t *testing.T, ce rout.ClaimedEntry) { assert.Empty(t, ce.Metadata()) },
		},
		{
			name:          "observability_oversize",
			metadata:      "{}",
			observability: bigJSONField("traceId", principalBranchOversizeLen),
			principal:     "{}",
			wantWarn:      "observability JSON exceeds max size",
			assert:        func(t *testing.T, ce rout.ClaimedEntry) { assert.Zero(t, ce.Observability()) },
		},
		{
			name:     "observability_unmarshal_failure",
			metadata: "{}", observability: "[]", principal: "{}",
			wantWarn: "failed to unmarshal observability",
			assert:   func(t *testing.T, ce rout.ClaimedEntry) { assert.Zero(t, ce.Observability()) },
		},
		{
			name:          "observability_validate_failure",
			metadata:      "{}",
			observability: invalidTraceParentJSON,
			principal:     "{}",
			wantWarn:      "observability fails validation",
			assert:        func(t *testing.T, ce rout.ClaimedEntry) { assert.Zero(t, ce.Observability()) },
		},
		{
			name:      "principal_oversize",
			metadata:  "{}", observability: "{}",
			principal: bigJSONField("actorId", principalBranchOversizeLen),
			wantWarn:  "principal JSON exceeds max size",
			assert:    func(t *testing.T, ce rout.ClaimedEntry) { assert.Zero(t, ce.Principal()) },
		},
		{
			name:      "principal_unmarshal_failure",
			metadata:  "{}", observability: "{}", principal: "[]",
			wantWarn: "failed to unmarshal principal",
			assert:   func(t *testing.T, ce rout.ClaimedEntry) { assert.Zero(t, ce.Principal()) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, truncErr := pool.DB().Exec(ctx, "TRUNCATE outbox_entries")
			require.NoError(t, truncErr, "TRUNCATE must succeed")

			id := "corrupt-" + tc.name
			insertCorruptOutboxRow(t, pool, id, tc.metadata, tc.observability, tc.principal)

			buf := installWarnCapture(t)

			claimed, err := store.ClaimPending(ctx, 10)
			require.NoError(t, err, "ClaimPending must not fail the batch on a corrupt column")
			require.Len(t, claimed, 1, "the corrupt row is still claimed")

			tc.assert(t, claimed[0])

			line := sloghelper.FindLogEntry(buf.String(), tc.wantWarn)
			require.NotNil(t, line, "expected a WARN line containing %q; got log:\n%s", tc.wantWarn, buf.String())
			assert.Equal(t, "WARN", line["level"], "drop must be logged at WARN")
			assert.Equal(t, id, line["entry_id"], "warn must carry entry_id correlation field")
			assert.Equal(t, "user.login", line["event_type"], "warn must carry event_type correlation field")
		})
	}
}

// TestPGOutboxStore_OccurredAt_PrecisionAndConstraint covers the two PG-specific
// occurred_at boundaries: timestamptz microsecond round-trip (distinct from
// created_at) and the migration-044 NOT NULL constraint.
func TestPGOutboxStore_OccurredAt_PrecisionAndConstraint(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()

	t.Run("MicrosecondRoundTrip", func(t *testing.T) {
		_, truncErr := pool.DB().Exec(ctx, "TRUNCATE outbox_entries")
		require.NoError(t, truncErr)

		// PG timestamptz has microsecond precision; truncate so the comparison is
		// exact and the skew keeps occurred_at strictly before created_at.
		occurredAt := time.Now().UTC().Truncate(time.Microsecond)
		createdAt := occurredAt.Add(occurredAtBranchSkew)

		const insertSQL = `INSERT INTO outbox_entries
			(id, aggregate_id, aggregate_type, event_type, topic, payload,
			 metadata, principal, occurred_at, created_at, status, attempts)
			VALUES ($1, '', '', $2, $3, $4, '{}'::jsonb, '{}'::jsonb, $5, $6, 'pending', 0)`
		_, err := pool.DB().Exec(ctx, insertSQL,
			"occurred-roundtrip", "user.login", "user.login",
			[]byte(`{"action":"login"}`), occurredAt, createdAt)
		require.NoError(t, err)

		store := NewOutboxStore(pool.DB(), clock.Real())
		claimed, err := store.ClaimPending(ctx, 10)
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		got := claimed[0]
		assert.True(t, got.OccurredAt().Equal(occurredAt),
			"occurred_at must round-trip at microsecond precision: got %v want %v",
			got.OccurredAt(), occurredAt)
		assert.True(t, got.CreatedAt().Equal(createdAt),
			"created_at must round-trip: got %v want %v", got.CreatedAt(), createdAt)
		assert.False(t, got.OccurredAt().Equal(got.CreatedAt()),
			"occurred_at must stay distinct from created_at")
	})

	t.Run("NotNullConstraint", func(t *testing.T) {
		_, truncErr := pool.DB().Exec(ctx, "TRUNCATE outbox_entries")
		require.NoError(t, truncErr)

		// migration 044 added occurred_at TIMESTAMPTZ NOT NULL with no DEFAULT;
		// an INSERT omitting it must be rejected by PG, not silently defaulted.
		const insertSQL = `INSERT INTO outbox_entries
			(id, aggregate_id, aggregate_type, event_type, topic, payload,
			 metadata, principal, created_at, status, attempts)
			VALUES ($1, '', '', $2, $3, $4, '{}'::jsonb, '{}'::jsonb, now(), 'pending', 0)`
		_, err := pool.DB().Exec(ctx, insertSQL,
			"occurred-missing", "user.login", "user.login", []byte(`{"action":"login"}`))
		require.Error(t, err, "omitting occurred_at must violate the NOT NULL constraint")
		assert.Contains(t, strings.ToLower(err.Error()), "occurred_at",
			"the constraint error should name the occurred_at column")
	})
}
