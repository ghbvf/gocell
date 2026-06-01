package correlate_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
)

// fakeQueryStore implements ledger.QueryStore for tests without importing adapters.
// It records the last AuditFilters passed to Query so callers can assert the
// filter was correctly wired.
type fakeQueryStore struct {
	entries        []*ledger.Entry
	queryErr       error
	lastFilters    ledger.AuditFilters
	lastFiltersSet bool
}

func (f *fakeQueryStore) Query(_ context.Context, filters ledger.AuditFilters, _ query.ListParams) ([]*ledger.Entry, error) {
	f.lastFilters = filters
	f.lastFiltersSet = true
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	var out []*ledger.Entry
	for _, e := range f.entries {
		if filters.TraceID != "" && e.TraceID != filters.TraceID {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// recordingStore wraps fakeQueryStore and exposes the captured filters.
// Used in tests that assert on the AuditFilters passed to the store.
type recordingStore struct {
	fakeQueryStore
}

func newTestStore(entries []*ledger.Entry, err error) ledger.QueryStore {
	return &fakeQueryStore{entries: entries, queryErr: err}
}

// newRecordingStore returns a *recordingStore so tests can read lastFilters.
func newRecordingStore(entries []*ledger.Entry, err error) *recordingStore {
	return &recordingStore{fakeQueryStore{entries: entries, queryErr: err}}
}

// --- helpers ---

func newService(t *testing.T, store ledger.QueryStore, topo correlation.Topology) *correlate.Service {
	t.Helper()
	svc, err := correlate.NewService(store, topo, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func sampleEntry(traceID string) *ledger.Entry {
	return &ledger.Entry{
		ID:            "entry-1",
		EventID:       "evid-0001-0001-0001-000000000001",
		EventType:     "user.login",
		ActorID:       "actor-abc",
		SubjectID:     "subject-xyz", // stored in ledger but NOT in the wire DTO
		CorrelationID: "corr-001",
		TraceID:       traceID,
		OccurredAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Timestamp:     time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
	}
}

// --- NewService ---

func TestNewService_NilStore(t *testing.T) {
	_, err := correlate.NewService(nil, correlation.Topology{}, nil)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
}

func TestNewService_NilInterfaceStore(t *testing.T) {
	// typed-nil interface: var store ledger.QueryStore = (*fakeQueryStore)(nil)
	var store ledger.QueryStore = (*fakeQueryStore)(nil)
	_, err := correlate.NewService(store, correlation.Topology{}, nil)
	if err == nil {
		t.Fatal("expected error for typed-nil store, got nil")
	}
}

func TestNewService_EmptyTopoOK(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, correlation.Topology{}, nil)
	if err != nil {
		t.Fatalf("NewService with empty topo: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestNewService_NilLogger(t *testing.T) {
	// nil logger should be replaced by slog.Default() — no panic
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService with nil logger: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

// --- CorrelateByTrace ---

func TestCorrelateByTrace_Found(t *testing.T) {
	traceID := "trace-abc123"
	entry := sampleEntry(traceID)
	store := newTestStore([]*ledger.Entry{entry}, nil)
	svc := newService(t, store, nil)

	result, err := svc.CorrelateByTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("CorrelateByTrace: %v", err)
	}
	if len(result.AuditEntries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.AuditEntries))
	}
	got := result.AuditEntries[0]
	if got.ID != entry.ID {
		t.Errorf("id: got %q, want %q", got.ID, entry.ID)
	}
	if got.EventType != entry.EventType {
		t.Errorf("eventType: got %q, want %q", got.EventType, entry.EventType)
	}
	if got.ActorID != entry.ActorID {
		t.Errorf("actorId: got %q, want %q", got.ActorID, entry.ActorID)
	}
	if got.CorrelationID != entry.CorrelationID {
		t.Errorf("correlationId: got %q, want %q", got.CorrelationID, entry.CorrelationID)
	}
	// subjectId is deliberately excluded from the DTO (end-user PII, not needed
	// for trace correlation). result.TraceID being accessible confirms the
	// exported TraceResult type is returned.
	_ = result.TraceID
}

func TestCorrelateByTrace_NotFound(t *testing.T) {
	store := newTestStore(nil, nil)
	svc := newService(t, store, nil)

	_, err := svc.CorrelateByTrace(context.Background(), "no-such-trace")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	ecErr := mustErrcode(t, err)
	if ecErr.Kind != errcode.KindNotFound {
		t.Errorf("kind: got %v, want KindNotFound", ecErr.Kind)
	}
}

func TestCorrelateByTrace_MultipleEntries(t *testing.T) {
	traceID := "trace-multi"
	entries := []*ledger.Entry{
		sampleEntry(traceID),
		{ID: "entry-2", EventType: "config.updated", TraceID: traceID, OccurredAt: time.Now(), Timestamp: time.Now()},
	}
	store := newTestStore(entries, nil)
	svc := newService(t, store, nil)

	result, err := svc.CorrelateByTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("CorrelateByTrace multi: %v", err)
	}
	if len(result.AuditEntries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.AuditEntries))
	}
}

func TestCorrelateByTrace_StoreError(t *testing.T) {
	storeErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "db error")
	store := newTestStore(nil, storeErr)
	svc := newService(t, store, nil)

	_, err := svc.CorrelateByTrace(context.Background(), "any-trace")
	if err == nil {
		t.Fatal("expected error from store, got nil")
	}
}

func TestCorrelateByTrace_ResultHasTraceID(t *testing.T) {
	traceID := "trace-xyz"
	store := newTestStore([]*ledger.Entry{sampleEntry(traceID)}, nil)
	svc := newService(t, store, nil)

	result, err := svc.CorrelateByTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TraceID != traceID {
		t.Errorf("result.TraceID: got %q, want %q", result.TraceID, traceID)
	}
}

// TestCorrelateByTrace_FilterWired asserts that CorrelateByTrace passes
// AuditFilters{TraceID: traceID} to the store. The store mixes two distinct
// trace IDs so a missing or wrong filter would return incorrect entries.
func TestCorrelateByTrace_FilterWired(t *testing.T) {
	wantTraceID := "trace-want-111"
	otherTraceID := "trace-other-222"
	entries := []*ledger.Entry{
		{ID: "e1", EventType: "a.done", TraceID: wantTraceID, OccurredAt: time.Now(), Timestamp: time.Now()},
		{ID: "e2", EventType: "b.done", TraceID: otherTraceID, OccurredAt: time.Now(), Timestamp: time.Now()},
		{ID: "e3", EventType: "a.started", TraceID: wantTraceID, OccurredAt: time.Now(), Timestamp: time.Now()},
	}
	rs := newRecordingStore(entries, nil)
	svc := newService(t, rs, nil)

	result, err := svc.CorrelateByTrace(context.Background(), wantTraceID)
	if err != nil {
		t.Fatalf("CorrelateByTrace: %v", err)
	}
	// Verify the filter was wired with the correct TraceID.
	if !rs.lastFiltersSet {
		t.Fatal("store Query was not called")
	}
	if rs.lastFilters.TraceID != wantTraceID {
		t.Errorf("AuditFilters.TraceID: got %q, want %q", rs.lastFilters.TraceID, wantTraceID)
	}
	// Only entries for wantTraceID must be returned (fake filters correctly).
	for _, ae := range result.AuditEntries {
		if ae.ID == "e2" {
			t.Errorf("entry e2 belongs to a different trace_id and must not appear in result")
		}
	}
	if len(result.AuditEntries) != 2 {
		t.Errorf("expected 2 entries for wantTraceID, got %d", len(result.AuditEntries))
	}
}

// TestCorrelateByTrace_HasMoreSentinel verifies that when the store returns
// traceQueryLimit+1 entries (N+1 hasMore sentinel), CorrelateByTrace trims the
// sentinel, sets HasMore=true, and Returned==traceQueryLimit.
func TestCorrelateByTrace_HasMoreSentinel(t *testing.T) {
	// traceQueryLimit is 500; produce 501 entries to trigger the sentinel path.
	const n = 501
	traceID := "trace-overflow"
	entries := make([]*ledger.Entry, n)
	for i := range entries {
		entries[i] = &ledger.Entry{
			ID:         fmt.Sprintf("e%d", i),
			EventType:  "tick",
			TraceID:    traceID,
			OccurredAt: time.Now(),
			Timestamp:  time.Now(),
		}
	}
	store := newTestStore(entries, nil)
	svc := newService(t, store, nil)

	result, err := svc.CorrelateByTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("CorrelateByTrace: %v", err)
	}
	if !result.HasMore {
		t.Error("HasMore: got false, want true")
	}
	if len(result.AuditEntries) != 500 {
		t.Errorf("AuditEntries count: got %d, want 500 (sentinel trimmed)", len(result.AuditEntries))
	}
	if result.Returned != 500 {
		t.Errorf("Returned: got %d, want 500", result.Returned)
	}
}

// TestCorrelateByTrace_HasMoreFalse verifies that when entries <= traceQueryLimit,
// HasMore is false and Returned equals the actual count.
func TestCorrelateByTrace_HasMoreFalse(t *testing.T) {
	traceID := "trace-small"
	entries := []*ledger.Entry{sampleEntry(traceID)}
	store := newTestStore(entries, nil)
	svc := newService(t, store, nil)

	result, err := svc.CorrelateByTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("CorrelateByTrace: %v", err)
	}
	if result.HasMore {
		t.Error("HasMore: got true, want false for single entry")
	}
	if result.Returned != 1 {
		t.Errorf("Returned: got %d, want 1", result.Returned)
	}
}

// TestCorrelateByTrace_EventIDPresent confirms that eventId is populated in the
// returned auditEntryDTOs.
func TestCorrelateByTrace_EventIDPresent(t *testing.T) {
	traceID := "trace-evid"
	entry := sampleEntry(traceID)
	entry.EventID = "evid-uuid-0000-0000-0000-000000000042"
	store := newTestStore([]*ledger.Entry{entry}, nil)
	svc := newService(t, store, nil)

	result, err := svc.CorrelateByTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("CorrelateByTrace: %v", err)
	}
	if len(result.AuditEntries) == 0 {
		t.Fatal("expected at least one audit entry")
	}
	got := result.AuditEntries[0].EventID
	if got != entry.EventID {
		t.Errorf("eventId: got %q, want %q", got, entry.EventID)
	}
}

// --- CorrelateByCell ---

func TestCorrelateByCell_Found(t *testing.T) {
	topo := correlation.Topology{
		"accesscore": correlation.CellOwner{Team: "platform", Role: "auth"},
	}
	store := newTestStore(nil, nil)
	svc := newService(t, store, topo)

	result, err := svc.CorrelateByCell(context.Background(), "accesscore")
	if err != nil {
		t.Fatalf("CorrelateByCell: %v", err)
	}
	if result.Owner.CellID != "accesscore" {
		t.Errorf("cellId: got %q, want %q", result.Owner.CellID, "accesscore")
	}
	if result.Owner.Team != "platform" {
		t.Errorf("team: got %q, want %q", result.Owner.Team, "platform")
	}
	if result.Owner.Role != "auth" {
		t.Errorf("role: got %q, want %q", result.Owner.Role, "auth")
	}
	if result.Selectors.Metric == "" {
		t.Error("metric selector must not be empty")
	}
	if result.Selectors.Alert == "" {
		t.Error("alert selector must not be empty")
	}
}

func TestCorrelateByCell_NotFound(t *testing.T) {
	topo := correlation.Topology{}
	store := newTestStore(nil, nil)
	svc := newService(t, store, topo)

	_, err := svc.CorrelateByCell(context.Background(), "unknown-cell")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	ecErr := mustErrcode(t, err)
	if ecErr.Kind != errcode.KindNotFound {
		t.Errorf("kind: got %v, want KindNotFound", ecErr.Kind)
	}
}

func TestCorrelateByCell_NilTopology(t *testing.T) {
	// nil topology should behave like empty (always miss)
	store := newTestStore(nil, nil)
	svc := newService(t, store, nil)

	_, err := svc.CorrelateByCell(context.Background(), "accesscore")
	if err == nil {
		t.Fatal("expected not-found error for nil topo, got nil")
	}
}

func TestCorrelateByCell_SelectorFormat(t *testing.T) {
	cellID := "configcore"
	topo := correlation.Topology{
		cellID: correlation.CellOwner{Team: "platform", Role: "config"},
	}
	store := newTestStore(nil, nil)
	svc := newService(t, store, topo)

	result, err := svc.CorrelateByCell(context.Background(), cellID)
	if err != nil {
		t.Fatalf("CorrelateByCell: %v", err)
	}
	wantMetric := `{cell="configcore"}`
	if result.Selectors.Metric != wantMetric {
		t.Errorf("metric selector: got %q, want %q", result.Selectors.Metric, wantMetric)
	}
	wantAlert := `alertname=~".+",cell="configcore"`
	if result.Selectors.Alert != wantAlert {
		t.Errorf("alert selector: got %q, want %q", result.Selectors.Alert, wantAlert)
	}
}

func TestCorrelateByCell_EmptyTeamRole(t *testing.T) {
	// CellOwner may have empty team/role — owner is still found
	topo := correlation.Topology{
		"minimalcell": correlation.CellOwner{},
	}
	store := newTestStore(nil, nil)
	svc := newService(t, store, topo)

	result, err := svc.CorrelateByCell(context.Background(), "minimalcell")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Owner.CellID != "minimalcell" {
		t.Errorf("cellId: got %q", result.Owner.CellID)
	}
}

// --- helpers ---

func mustErrcode(t *testing.T, err error) *errcode.Error {
	t.Helper()
	var e *errcode.Error
	if errors.As(err, &e) {
		return e
	}
	t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	return nil
}
