package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// TestAuditCrossTenantStore_Constructor verifies nil-pool rejection at construction.
func TestAuditCrossTenantStore_Constructor(t *testing.T) {
	_, err := NewAuditCrossTenantStore(nil)
	if err == nil {
		t.Fatal("NewAuditCrossTenantStore(nil) must return an error")
	}
}

// TestCrossTenantSQL_NoTenantOrNamespacePredicate verifies that the SQL produced
// by the cross-tenant builder contains no namespace or tenant_id predicates.
// This is a unit-level test that does not require a PG connection; it exercises
// the predicate-free design invariant (#1810) that distinguishes the admin-pool
// read from the ordinary serving Store.Query path.
func TestCrossTenantSQL_NoTenantOrNamespacePredicate(t *testing.T) {
	cases := []struct {
		name    string
		filters ledger.AuditFilters
		params  query.ListParams
	}{
		{
			name:    "no filters first page",
			filters: ledger.AuditFilters{},
			params:  query.ListParams{Limit: 10, Sort: ledger.QuerySort()},
		},
		{
			name:    "EventType filter first page",
			filters: ledger.AuditFilters{EventType: "audit.login"},
			params:  query.ListParams{Limit: 10, Sort: ledger.QuerySort()},
		},
		{
			name:    "ActorID filter first page",
			filters: ledger.AuditFilters{ActorID: "alice"},
			params:  query.ListParams{Limit: 10, Sort: ledger.QuerySort()},
		},
		{
			name:    "time range filter first page",
			filters: ledger.AuditFilters{From: time.Now().Add(-time.Hour), To: time.Now()},
			params:  query.ListParams{Limit: 10, Sort: ledger.QuerySort()},
		},
		{
			name:    "with cursor (next page)",
			filters: ledger.AuditFilters{},
			params: query.ListParams{
				Limit:        10,
				Sort:         ledger.QuerySort(),
				CursorValues: []any{time.Now().Format(time.RFC3339Nano), "some-uuid"},
			},
		},
		{
			name:    "all filters combined",
			filters: ledger.AuditFilters{EventType: "audit.read", ActorID: "bob", SubjectID: "alice"},
			params:  query.ListParams{Limit: 20, Sort: ledger.QuerySort()},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, err := crossTenantSQLForTest(tc.filters, tc.params)
			if err != nil {
				t.Fatalf("crossTenantSQLForTest: %v", err)
			}
			if !crossTenantSQLHasNoTenantPredicate(sql) {
				t.Errorf("SQL contains namespace or tenant_id predicate:\n%s", sql)
			}
			// Verify SELECT and FROM audit_entries are present.
			if !strings.Contains(sql, "FROM audit_entries") {
				t.Errorf("SQL missing FROM audit_entries:\n%s", sql)
			}
			// Verify ORDER BY is present (keyset always appends it).
			if !strings.Contains(strings.ToLower(sql), "order by") {
				t.Errorf("SQL missing ORDER BY:\n%s", sql)
			}
		})
	}
}

// TestCrossTenantSQL_FilterPredicatesPresent verifies that filter predicates
// appear in the SQL when the corresponding AuditFilters fields are non-zero.
func TestCrossTenantSQL_FilterPredicatesPresent(t *testing.T) {
	cases := []struct {
		name        string
		filters     ledger.AuditFilters
		wantContain string
	}{
		{
			name:        "event_type filter",
			filters:     ledger.AuditFilters{EventType: "audit.login"},
			wantContain: "event_type",
		},
		{
			name:        "actor_id filter",
			filters:     ledger.AuditFilters{ActorID: "alice"},
			wantContain: "actor_id",
		},
		{
			name:        "subject_id filter",
			filters:     ledger.AuditFilters{SubjectID: "target-user"},
			wantContain: "subject_id",
		},
		{
			name:        "trace_id filter",
			filters:     ledger.AuditFilters{TraceID: "abc123"},
			wantContain: "trace_id",
		},
		{
			name:        "from timestamp",
			filters:     ledger.AuditFilters{From: time.Now()},
			wantContain: "timestamp >=",
		},
		{
			name:        "to timestamp",
			filters:     ledger.AuditFilters{To: time.Now()},
			wantContain: "timestamp <=",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, err := crossTenantSQLForTest(tc.filters, query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
			if err != nil {
				t.Fatalf("crossTenantSQLForTest: %v", err)
			}
			if !strings.Contains(sql, tc.wantContain) {
				t.Errorf("SQL missing %q:\n%s", tc.wantContain, sql)
			}
		})
	}
}

// TestCrossTenantSQL_EmptySortRejected verifies that crossTenantSQLForTest
// propagates the ErrValidationFailed from AppendKeyset when Sort is empty.
func TestCrossTenantSQL_EmptySortRejected(t *testing.T) {
	_, err := crossTenantSQLForTest(ledger.AuditFilters{}, query.ListParams{Limit: 10})
	if err == nil {
		t.Fatal("empty Sort must return an error")
	}
}
