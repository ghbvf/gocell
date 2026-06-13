package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
)

// tenantA / tenantB are canonical UUIDs for cross-tenant mem-store tests.
const (
	ctMemTenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	ctMemTenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// Per-entry timestamp offsets for deterministic seed ordering (TEST-TIME-LITERAL-01:
// no inline N*time.Millisecond literals at the seed call sites).
const (
	ctMs1 = 1 * time.Millisecond
	ctMs2 = 2 * time.Millisecond
	ctMs3 = 3 * time.Millisecond
	ctMs4 = 4 * time.Millisecond
)

// newTestMemStore builds a fresh MemStore backed by the test protocol.
func newTestMemStore(t *testing.T) (*ledger.MemStore, *clockmock.FakeClock) {
	t.Helper()
	fc := clockmock.New(storetest.EpochAnchor())
	proto := storetest.NewTestProtocol(t)
	store, err := ledger.NewMemStore(proto, fc)
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	return store, fc
}

// appendEntry appends one entry to the given MemStore. Fatals on error.
func appendEntry(t *testing.T, store *ledger.MemStore, eventID, tenantID, actorID, eventType string, ts time.Time) {
	t.Helper()
	e := &ledger.Entry{
		EventID:   eventID,
		EventType: eventType,
		ActorID:   actorID,
		TenantID:  tenantID,
		Timestamp: ts,
		Payload:   []byte(`{}`),
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("Append %s: %v", eventID, err)
	}
}

// TestNewMemCrossTenantStore_NilPool verifies nil/empty constructor rejections,
// including the typed-nil *MemStore case that is only caught at runtime
// (a (*MemStore)(nil) passes static type checking but must be rejected by the
// nil-guard in NewMemCrossTenantStore, which checks both validation.IsNilInterface
// and the concrete-pointer nil branch).
func TestNewMemCrossTenantStore_NilPool(t *testing.T) {
	cases := []struct {
		name   string
		stores []*ledger.MemStore
	}{
		{
			name:   "no stores (empty variadic)",
			stores: nil,
		},
		{
			// Typed-nil: (*MemStore)(nil) has a concrete type at compile time but
			// carries a nil pointer at runtime. The constructor must reject this
			// via the nil guard (validation.IsNilInterface branch + concrete == nil
			// branch) and return ErrValidationFailed, not panic or succeed.
			name:   "typed_nil_store",
			stores: []*ledger.MemStore{(*ledger.MemStore)(nil)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ledger.NewMemCrossTenantStore(tc.stores...)
			errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
		})
	}
}

// TestMemCrossTenantStore_CrossTenantRead verifies that QueryCrossTenant returns
// entries from multiple tenants across multiple MemStores.
func TestMemCrossTenantStore_CrossTenantRead(t *testing.T) {
	storeA, fc := newTestMemStore(t)
	storeB, _ := newTestMemStore(t)

	base := fc.Now()
	// Seed: 2 entries in tenant-A (storeA), 1 in tenant-B (storeB), 1 in tenant-B (storeA).
	appendEntry(t, storeA, "evt-a1", ctMemTenantA, "alice", "ct.test", base.Add(ctMs1))
	appendEntry(t, storeA, "evt-b1", ctMemTenantB, "bob", "ct.test", base.Add(ctMs2))
	appendEntry(t, storeB, "evt-a2", ctMemTenantA, "alice", "ct.test", base.Add(ctMs3))
	appendEntry(t, storeB, "evt-b2", ctMemTenantB, "charlie", "ct.other", base.Add(ctMs4))

	ct, err := ledger.NewMemCrossTenantStore(storeA, storeB)
	if err != nil {
		t.Fatalf("NewMemCrossTenantStore: %v", err)
	}

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := ct.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant: %v", err)
	}

	// All 4 entries must be visible.
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}

	// Both tenants must appear.
	tenantsSeen := make(map[string]bool)
	for _, r := range rows {
		tenantsSeen[r.TenantID] = true
	}
	for _, wantTenant := range []string{ctMemTenantA, ctMemTenantB} {
		if !tenantsSeen[wantTenant] {
			t.Errorf("tenant %q not found in cross-tenant results", wantTenant)
		}
	}

	// timestamp DESC order.
	for i := 1; i < len(rows); i++ {
		if rows[i].Timestamp.After(rows[i-1].Timestamp) {
			t.Errorf("ordering violation at index %d", i)
		}
	}
}

// TestMemCrossTenantStore_Pagination verifies keyset pagination yields all entries
// exactly once.
func TestMemCrossTenantStore_Pagination(t *testing.T) {
	storeA, fc := newTestMemStore(t)
	storeB, _ := newTestMemStore(t)

	base := fc.Now()
	for i := 0; i < 5; i++ {
		tid := ctMemTenantA
		if i%2 == 0 {
			tid = ctMemTenantB
		}
		target := storeA
		if i >= 3 {
			target = storeB
		}
		appendEntry(t, target, "pg-evt-"+string(rune('0'+i)), tid, "actor", "pg.test",
			base.Add(time.Duration(i)*time.Millisecond))
	}

	ct, err := ledger.NewMemCrossTenantStore(storeA, storeB)
	if err != nil {
		t.Fatalf("NewMemCrossTenantStore: %v", err)
	}

	ctv := tenant.NewCrossTenantVisibility()
	var collected []string
	var cursorVals []any
	const pageSize = 2
	for iter := 0; iter < 10; iter++ {
		rows, err := ct.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
			query.ListParams{Limit: pageSize, Sort: ledger.QuerySort(), CursorValues: cursorVals})
		if err != nil {
			t.Fatalf("QueryCrossTenant iter %d: %v", iter, err)
		}
		hasMore := len(rows) > pageSize
		page := rows
		if hasMore {
			page = rows[:pageSize]
		}
		for _, e := range page {
			collected = append(collected, e.EventID)
		}
		if !hasMore {
			break
		}
		last := page[len(page)-1]
		cursorVals = []any{last.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"), last.ID}
	}

	if len(collected) != 5 {
		t.Fatalf("Pagination: traversed %d entries, want 5; got=%v", len(collected), collected)
	}
	seen := make(map[string]bool)
	for _, id := range collected {
		if seen[id] {
			t.Errorf("Pagination: duplicate EventID %q", id)
		}
		seen[id] = true
	}
}

// TestMemCrossTenantStore_FilterEventType verifies EventType narrowing.
func TestMemCrossTenantStore_FilterEventType(t *testing.T) {
	storeA, fc := newTestMemStore(t)
	base := fc.Now()
	appendEntry(t, storeA, "fe-1", ctMemTenantA, "actor", "type.X", base)
	appendEntry(t, storeA, "fe-2", ctMemTenantA, "actor", "type.Y", base.Add(time.Millisecond))
	appendEntry(t, storeA, "fe-3", ctMemTenantB, "actor", "type.X", base.Add(ctMs2))

	ct, err := ledger.NewMemCrossTenantStore(storeA)
	if err != nil {
		t.Fatalf("NewMemCrossTenantStore: %v", err)
	}

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := ct.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{EventType: "type.X"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("EventType filter: got %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.EventType != "type.X" {
			t.Errorf("EventType filter leaked %q", r.EventType)
		}
	}
}

// TestMemCrossTenantStore_EmptySortRejected verifies that an empty Sort yields
// ErrValidationFailed.
func TestMemCrossTenantStore_EmptySortRejected(t *testing.T) {
	storeA, _ := newTestMemStore(t)
	ct, err := ledger.NewMemCrossTenantStore(storeA)
	if err != nil {
		t.Fatalf("NewMemCrossTenantStore: %v", err)
	}

	ctv := tenant.NewCrossTenantVisibility()
	_, err = ct.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{},
		query.ListParams{Limit: 10})
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestMemCrossTenantStore_ActorIDFilter verifies ActorID narrowing across tenants.
func TestMemCrossTenantStore_ActorIDFilter(t *testing.T) {
	storeA, fc := newTestMemStore(t)
	storeB, _ := newTestMemStore(t)
	base := fc.Now()
	appendEntry(t, storeA, "af-1", ctMemTenantA, "alice", "af.test", base)
	appendEntry(t, storeA, "af-2", ctMemTenantB, "bob", "af.test", base.Add(time.Millisecond))
	appendEntry(t, storeB, "af-3", ctMemTenantA, "alice", "af.test", base.Add(ctMs2))

	ct, err := ledger.NewMemCrossTenantStore(storeA, storeB)
	if err != nil {
		t.Fatalf("NewMemCrossTenantStore: %v", err)
	}

	ctv := tenant.NewCrossTenantVisibility()
	rows, err := ct.QueryCrossTenant(context.Background(), ctv,
		ledger.AuditFilters{ActorID: "alice"},
		query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	if err != nil {
		t.Fatalf("QueryCrossTenant(ActorID=alice): %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ActorID filter: got %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.ActorID != "alice" {
			t.Errorf("ActorID filter leaked %q", r.ActorID)
		}
	}
}

// TestMemCrossTenantStore_ConformanceSuite runs the shared cross-tenant conformance
// suite against MemCrossTenantStore, ensuring the mem backend satisfies the same
// contract as the PG backend.
func TestMemCrossTenantStore_ConformanceSuite(t *testing.T) {
	proto := storetest.NewTestProtocol(t)

	factory := storetest.CrossTenantFactory(func(t *testing.T, seed []*ledger.Entry) (ledger.CrossTenantQueryStore, func()) {
		t.Helper()
		fc := clockmock.New(storetest.EpochAnchor())
		// Two mem stores simulate two namespace chains (relay + bootstrap).
		relayStore, err := ledger.NewMemStore(proto, fc)
		if err != nil {
			t.Fatalf("NewMemStore relay: %v", err)
		}
		bootstrapStore, err := ledger.NewMemStore(proto, fc)
		if err != nil {
			t.Fatalf("NewMemStore bootstrap: %v", err)
		}

		// Seed entries: alternate between relay and bootstrap stores to exercise
		// both namespace chains.
		for i, e := range seed {
			target := relayStore
			if i%2 != 0 {
				target = bootstrapStore
			}
			cp := *e
			if err := target.Append(context.Background(), &cp); err != nil {
				t.Fatalf("seed Append %s: %v", e.EventID, err)
			}
		}

		ct, err := ledger.NewMemCrossTenantStore(relayStore, bootstrapStore)
		if err != nil {
			t.Fatalf("NewMemCrossTenantStore: %v", err)
		}
		return ct, func() {} // mem store has no cleanup
	})

	storetest.RunCrossTenantQueryConformance(t, factory)
}
