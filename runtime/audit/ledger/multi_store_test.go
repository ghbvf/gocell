package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Conformance suite for ledger.MultiStore — the read-side aggregator that
// fans Query across multiple Stores (issue #1121 / ADR 202605270230).

// Test-data timestamp offsets used by the interleaving fixtures below.
// File-local package-level consts (not pkg/testutil/testtime.* — these are
// site-specific test fixture data, not cross-cutting timeouts) per
// TEST-TIME-LITERAL-01.
const (
	offset5s  = 5 * time.Second
	offset10s = 10 * time.Second
	offset20s = 20 * time.Second
	offset30s = 30 * time.Second
	offset40s = 40 * time.Second
	offset50s = 50 * time.Second
	offset60s = 60 * time.Second
)

func mustNamespace(t *testing.T, s string) ledger.NamespaceID {
	t.Helper()
	ns, err := ledger.ParseNamespaceID(s)
	require.NoError(t, err, "namespace %q must validate", s)
	return ns
}

func buildMemStore(t *testing.T, ns ledger.NamespaceID, clk clock.Clock) *ledger.MemStore {
	t.Helper()
	p, err := ledger.NewProtocol(
		ns,
		[]byte("multi-store-test-hmac-32bytes!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "protocol")
	store, err := ledger.NewMemStore(p, clk)
	require.NoError(t, err, "memstore")
	return store
}

// appendAt seeds an Entry with the supplied event id / type / actor / timestamp.
// Payload is a minimal valid JSON object so the strict-mode payload check
// passes uniformly across stores.
func appendAt(t *testing.T, store *ledger.MemStore, eventID, eventType, actorID string, ts time.Time) {
	t.Helper()
	payload := mustJSON(t, map[string]any{"i": eventID})
	err := store.Append(context.Background(), &ledger.Entry{
		EventID:   eventID,
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: ts,
		Payload:   payload,
	})
	require.NoError(t, err, "Append %s", eventID)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// TestNewMultiStore_RequiresAtLeastTwoStores asserts the constructor's
// "single-store is just an awkward indirection" guard.
func TestNewMultiStore_RequiresAtLeastTwoStores(t *testing.T) {
	t.Parallel()
	t.Run("zero stores", func(t *testing.T) {
		t.Parallel()
		_, err := ledger.NewMultiStore()
		require.Error(t, err)
	})
	t.Run("one store", func(t *testing.T) {
		t.Parallel()
		s := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(time.Now()))
		_, err := ledger.NewMultiStore(s)
		require.Error(t, err)
	})
}

// TestNewMultiStore_RejectsNilStore asserts the nil-interface guard.
func TestNewMultiStore_RejectsNilStore(t *testing.T) {
	t.Parallel()
	good := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(time.Now()))
	_, err := ledger.NewMultiStore(good, nil)
	require.Error(t, err, "nil store must be rejected")
}

// TestMultiStore_Query_RequiresSort asserts the same sort-required contract
// every Store implementation enforces.
func TestMultiStore_Query_RequiresSort(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clk)
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clk)
	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)

	_, qerr := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{Limit: 5})
	require.Error(t, qerr, "empty sort must be rejected")
	var coded *errcode.Error
	require.True(t, errors.As(qerr, &coded))
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}

// TestMultiStore_Query_MergesAcrossNamespaces is the core invariant for
// issue #1121: entries written to two different chains must all appear in a
// single Query result, ordered by (timestamp DESC, id ASC).
func TestMultiStore_Query_MergesAcrossNamespaces(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(base))
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clockmock.New(base))

	// Interleave timestamps across the two stores so a single-namespace read
	// would observe a strict subset; only the merge can return all five.
	appendAt(t, a, "evt-a1", "event.user.created.v1", "user:1", base.Add(offset50s))
	appendAt(t, b, "evt-b1", "bootstrap.auth.fail", "system:bootstrap", base.Add(offset40s))
	appendAt(t, a, "evt-a2", "event.session.created.v1", "user:1", base.Add(offset30s))
	appendAt(t, b, "evt-b2", "bootstrap.auth.fail", "system:bootstrap", base.Add(offset20s))
	appendAt(t, a, "evt-a3", "event.user.locked.v1", "user:1", base.Add(offset10s))

	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)
	got, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 10,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	require.Len(t, got, 5, "merge must surface every entry across both chains")
	// Newest first ordering: a1 (50s) → b1 (40s) → a2 (30s) → b2 (20s) → a3 (10s).
	wantIDs := []string{"evt-a1", "evt-b1", "evt-a2", "evt-b2", "evt-a3"}
	for i, want := range wantIDs {
		assert.Equalf(t, want, got[i].EventID, "merged position %d", i)
	}
}

// TestMultiStore_Query_FiltersByEventType is the regression guard for the
// #1121 ssobff reproducer: filtering by `eventType=bootstrap.auth.fail` must
// return entries from the bootstrap chain even when the auditcore chain has
// no such entries.
func TestMultiStore_Query_FiltersByEventType(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(base))
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clockmock.New(base))

	appendAt(t, a, "evt-user", "event.user.created.v1", "user:1", base.Add(offset20s))
	appendAt(t, b, "evt-fail1", "bootstrap.auth.fail", "system:bootstrap", base.Add(offset10s))
	appendAt(t, b, "evt-fail2", "bootstrap.auth.fail", "system:bootstrap", base.Add(offset5s))

	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)
	got, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(),
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	require.NoError(t, err)
	require.Len(t, got, 2, "filter must surface only bootstrap.auth.fail entries from the bootstrap chain")
	assert.Equal(t, "evt-fail1", got[0].EventID, "newer entry must come first")
	assert.Equal(t, "evt-fail2", got[1].EventID)
}

// TestMultiStore_Query_HonorsFetchLimit asserts that the aggregator returns
// at most FetchLimit() rows so query.ExecutePagedQuery's N+1 hasMore detection
// still works through the fan-out layer.
func TestMultiStore_Query_HonorsFetchLimit(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(base))
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clockmock.New(base))

	// 4 entries per store, 8 total, interleaved by timestamp.
	for i := 0; i < 4; i++ {
		appendAt(t, a, fmt.Sprintf("a%d", i), "event.x.v1", "actor", base.Add(time.Duration(2*i)*time.Second))
		appendAt(t, b, fmt.Sprintf("b%d", i), "event.y.v1", "actor", base.Add(time.Duration(2*i+1)*time.Second))
	}

	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)
	got, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 3,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	require.LessOrEqual(t, len(got), 4, "MultiStore must trim to FetchLimit()=Limit+1; got %d", len(got))
	require.GreaterOrEqual(t, len(got), 3, "MultiStore must return enough rows for N+1 hasMore detection")
}

// TestMultiStore_Query_OneEmptyOneNonEmpty asserts the aggregator returns the
// non-empty store's entries when one backing store has no rows. Empty stores
// must contribute zero rows, not propagate an error.
func TestMultiStore_Query_OneEmptyOneNonEmpty(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(base))
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clockmock.New(base))

	appendAt(t, a, "evt-a1", "event.user.created.v1", "user:1", base.Add(offset10s))

	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)
	got, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 10,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "non-empty chain's single entry must surface even when sibling chain is empty")
	assert.Equal(t, "evt-a1", got[0].EventID)
}

// TestMultiStore_Query_BothEmpty asserts an all-empty fan-out returns the
// canonical zero-rows shape (empty non-nil slice + no error), matching what
// query.ExecutePagedQuery expects from a single empty Store.
func TestMultiStore_Query_BothEmpty(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clk)
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clk)
	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)

	got, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 10,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	require.NotNil(t, got, "empty result must be a non-nil slice for parity with single-store Query")
	assert.Len(t, got, 0)
}

// TestMultiStore_Query_TiedTimestamps_TieBreakedByID asserts the id-ASC
// tie-break works across stores. Two entries with identical Timestamps but
// different IDs must order by id ascending — load-bearing for cursor
// stability across the fan-out boundary.
func TestMultiStore_Query_TiedTimestamps_TieBreakedByID(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(ts))
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clockmock.New(ts))

	// MemStore assigns ID = EventID. Pick IDs that lexicographically sort
	// "evt-a" < "evt-z" so the tie-break is observable.
	appendAt(t, a, "evt-a", "event.x.v1", "actor", ts)
	appendAt(t, b, "evt-z", "event.y.v1", "actor", ts)

	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)
	got, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 10,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	// QuerySort = (timestamp DESC, id ASC). Equal timestamps → id ASC: evt-a < evt-z.
	assert.Equal(t, "evt-a", got[0].EventID, "tie-break must be id ASC across stores")
	assert.Equal(t, "evt-z", got[1].EventID)
}

// TestMultiStore_Query_AppliesCursor exercises the cross-store cursor
// behavior: page 2 must skip the page-1 results from every backing store.
func TestMultiStore_Query_AppliesCursor(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	a := buildMemStore(t, mustNamespace(t, "auditcore"), clockmock.New(base))
	b := buildMemStore(t, mustNamespace(t, "bootstrap"), clockmock.New(base))

	// 6 entries with strictly-decreasing timestamps: a1(60), b1(50), a2(40),
	// b2(30), a3(20), b3(10). DESC-sorted full order = [a1, b1, a2, b2, a3, b3].
	appendAt(t, a, "a1", "event.x.v1", "actor", base.Add(offset60s))
	appendAt(t, b, "b1", "event.y.v1", "actor", base.Add(offset50s))
	appendAt(t, a, "a2", "event.x.v1", "actor", base.Add(offset40s))
	appendAt(t, b, "b2", "event.y.v1", "actor", base.Add(offset30s))
	appendAt(t, a, "a3", "event.x.v1", "actor", base.Add(offset20s))
	appendAt(t, b, "b3", "event.y.v1", "actor", base.Add(offset10s))

	ms, err := ledger.NewMultiStore(a, b)
	require.NoError(t, err)

	// Page 1 (Limit=2): expect [a1, b1] plus one extra for N+1 hasMore.
	page1, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 2,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(page1), 2)
	assert.Equal(t, "a1", page1[0].EventID, "page 1 must start at newest")
	assert.Equal(t, "b1", page1[1].EventID, "page 1 second entry")

	// Page 2 cursor: take the second result (b1) and walk forward.
	cursor := page1[1] // a1, b1 → cursor on b1
	page2, err := ms.Query(context.Background(), tenant.TenantID(""), memTestVis(), ledger.AuditFilters{}, query.ListParams{
		Limit: 2,
		Sort:  ledger.QuerySort(),
		CursorValues: []any{
			cursor.Timestamp.Format(time.RFC3339Nano),
			cursor.ID,
		},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(page2), 2)
	assert.Equal(t, "a2", page2[0].EventID, "page 2 must start strictly after the cursor")
	assert.Equal(t, "b2", page2[1].EventID)
}
