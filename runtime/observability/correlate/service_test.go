package correlate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
)

// fakeQueryStore implements ledger.QueryStore for tests without importing adapters.
type fakeQueryStore struct {
	entries  []*ledger.Entry
	queryErr error
}

func (f *fakeQueryStore) Query(_ context.Context, filters ledger.AuditFilters, _ query.ListParams) ([]*ledger.Entry, error) {
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

func newTestStore(entries []*ledger.Entry, err error) ledger.QueryStore {
	return &fakeQueryStore{entries: entries, queryErr: err}
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
		EventType:     "user.login",
		ActorID:       "actor-abc",
		SubjectID:     "subject-xyz",
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
	if got.SubjectID != entry.SubjectID {
		t.Errorf("subjectId: got %q, want %q", got.SubjectID, entry.SubjectID)
	}
	if got.CorrelationID != entry.CorrelationID {
		t.Errorf("correlationId: got %q, want %q", got.CorrelationID, entry.CorrelationID)
	}
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
