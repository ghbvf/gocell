package auditquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

const (
	auditNs100 = 100 * time.Nanosecond
	auditNs200 = 200 * time.Nanosecond
	// svcQueryTenant is the canonical tenant UUID the Service.Query tests pass.
	// Service.Query rejects an empty/non-canonical tenant at the post-auth boundary
	// (#1618 F2), so pagination/filter tests use a real tenant. Seeded entries are
	// tenant-less system rows (seedEntry sets no TenantID), which a canonical-tenant
	// query still returns via the `OR tenant_id=''` system-rows predicate.
	svcQueryTenant = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
)

// svcTenant is a short alias for the canonical Service.Query test tenant, keeping
// the (already long) svc.Query(...) call sites within the line-length budget.
var svcTenant = tenant.TenantID(svcQueryTenant)

// testTenantVis returns a RowScopeTenant RowVisibility for service_test helpers.
// All tests that call svc.Query use tenant-wide scope (no actor filter)
// to preserve pre-PR-4 test semantics.
func testTenantVis() tenant.RowVisibility {
	vis, _ := tenant.NewRowVisibility(tenant.RowScopeTenant, "")
	return vis
}

// testSelfVis returns a RowScopeSelf RowVisibility scoped to subject — used by the
// row-obligation cursor-scope replay test to mint a cursor under one owner scope.
func testSelfVis(subject string) tenant.RowVisibility {
	vis, _ := tenant.NewRowVisibility(tenant.RowScopeSelf, subject)
	return vis
}

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

func newTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		ns,
		[]byte("test-hmac-key-32bytes-long!!!!!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	return p
}

func newTestStore(t testing.TB) *ledger.MemStore {
	t.Helper()
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	return store
}

func newTestService() (*Service, *ledger.MemStore) {
	p, err := ledger.NewProtocol(
		ledger.NamespaceID("auditcore"),
		[]byte("test-hmac-key-32bytes-long!!!!!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		panic(err)
	}
	store, err := ledger.NewMemStore(p, clock.Real())
	if err != nil {
		panic(err)
	}
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc, store
}

// spyTxCtxKey marks a context as having passed through spyTxRunner.RunInTx, so a
// downstream store can prove it executed inside the tenant-scoped transaction.
type spyTxCtxKey struct{}

// spyTxRunner is a persistence.TxRunner that records that RunInTx was invoked and
// tags the closure's txCtx with spyTxCtxKey. When failWith is set it returns that
// error WITHOUT invoking the closure (modeling a tx-open failure before the read).
type spyTxRunner struct {
	called   bool
	failWith error
}

func (s *spyTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.called = true
	if s.failWith != nil {
		return s.failWith
	}
	return fn(context.WithValue(ctx, spyTxCtxKey{}, true))
}

// spyQueryStore is a ledger.QueryStore that records whether the ctx it received
// carried the spyTxRunner marker — i.e. whether Service.Query wrapped the store
// read in RunInTx (so FORCE RLS app.tenant_id GUC would be active, #1618).
type spyQueryStore struct {
	queried  bool
	sawTxCtx bool
}

func (s *spyQueryStore) Query(
	ctx context.Context, _ tenant.TenantID, _ tenant.RowVisibility,
	_ ledger.AuditFilters, _ query.ListParams,
) ([]*ledger.Entry, error) {
	s.queried = true
	if v, _ := ctx.Value(spyTxCtxKey{}).(bool); v {
		s.sawTxCtx = true
	}
	return []*ledger.Entry{}, nil
}

// TestService_Query_RunsStoreInsideRunInTx proves the store read executes inside
// the tenant-scoped RunInTx (#1618 F3): without it, FORCE RLS's app.tenant_id GUC
// would not be set on the read connection and the DB-Hard tenant backstop would be
// silently bypassed. The pre-fix suite used a passthrough DemoCellTxManager, so
// this invariant was unasserted.
func TestService_Query_RunsStoreInsideRunInTx(t *testing.T) {
	spyStore := &spyQueryStore{}
	spyTx := &spyTxRunner{}
	svc, err := NewService(spyStore, testCodec(), slog.Default(),
		persistence.WrapForCell(spyTx), query.RunModeProd)
	require.NoError(t, err)

	_, err = svc.Query(context.Background(), svcTenant, testTenantVis(),
		ledger.AuditFilters{}, query.PageParams{})
	require.NoError(t, err)
	assert.True(t, spyTx.called, "Service.Query must invoke RunInTx")
	assert.True(t, spyStore.queried, "store.Query must be called")
	assert.True(t, spyStore.sawTxCtx,
		"store.Query must run inside the RunInTx txCtx so FORCE RLS app.tenant_id GUC is active")
}

// TestService_Query_RunInTxError_Propagates locks the fail-closed path: when the
// tenant-scoped tx cannot be opened, the error propagates and the store is never
// queried outside a transaction (#1618 F3).
func TestService_Query_RunInTxError_Propagates(t *testing.T) {
	spyStore := &spyQueryStore{}
	boom := errcode.New(errcode.KindUnavailable, errcode.ErrInternal, "tx open failed")
	spyTx := &spyTxRunner{failWith: boom}
	svc, err := NewService(spyStore, testCodec(), slog.Default(),
		persistence.WrapForCell(spyTx), query.RunModeProd)
	require.NoError(t, err)

	_, err = svc.Query(context.Background(), svcTenant, testTenantVis(),
		ledger.AuditFilters{}, query.PageParams{})
	require.Error(t, err)
	assert.True(t, spyTx.called, "RunInTx must have been attempted")
	assert.False(t, spyStore.queried,
		"store must not be queried when the tenant-scoped tx fails to open")
}

func seedEntry(store *ledger.MemStore, id, eventType, actorID string, ts time.Time) {
	e := &ledger.Entry{
		ID:        id,
		EventID:   "evt-" + id,
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: ts,
		Payload:   []byte("{}"),
	}
	_ = store.Append(context.Background(), e)
}

func TestNewService_NilStore_ReturnsError(t *testing.T) {
	svc, err := NewService(nil, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.Error(t, err)
	assert.Nil(t, svc)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
}

func TestNewService_NilCodec_ReturnsError(t *testing.T) {
	store := newTestStore(t)
	svc, err := NewService(store, nil, slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.Error(t, err)
	assert.Nil(t, svc)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
}

func TestService_Query(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		seed    func(*ledger.MemStore)
		filters ledger.AuditFilters
		wantLen int
	}{
		{
			name:    "empty repository",
			seed:    func(_ *ledger.MemStore) {},
			filters: ledger.AuditFilters{},
			wantLen: 0,
		},
		{
			name: "all entries",
			seed: func(r *ledger.MemStore) {
				seedEntry(r, "a-1", "event.user.created.v1", "usr-1", now)
				seedEntry(r, "a-2", "event.session.created.v1", "usr-1", now.Add(time.Second))
			},
			filters: ledger.AuditFilters{},
			wantLen: 2,
		},
		{
			name: "filter by event type",
			seed: func(r *ledger.MemStore) {
				seedEntry(r, "a-1", "event.user.created.v1", "usr-1", now)
				seedEntry(r, "a-2", "event.session.created.v1", "usr-2", now.Add(time.Second))
			},
			filters: ledger.AuditFilters{EventType: "event.user.created.v1"},
			wantLen: 1,
		},
		{
			name: "filter by actor",
			seed: func(r *ledger.MemStore) {
				seedEntry(r, "a-1", "event.user.created.v1", "usr-1", now)
				seedEntry(r, "a-2", "event.user.created.v1", "usr-2", now.Add(time.Second))
			},
			filters: ledger.AuditFilters{ActorID: "usr-1"},
			wantLen: 1,
		},
		{
			name: "filter by subject (#1290)",
			seed: func(r *ledger.MemStore) {
				_ = r.Append(context.Background(), &ledger.Entry{
					ID: "s-1", EventID: "evt-s-1", EventType: "event.user.created.v1",
					ActorID: "actor-1", SubjectID: "alice", Timestamp: now, Payload: []byte("{}"),
				})
				_ = r.Append(context.Background(), &ledger.Entry{
					ID: "s-2", EventID: "evt-s-2", EventType: "event.user.created.v1",
					ActorID: "actor-2", SubjectID: "bob", Timestamp: now.Add(time.Second), Payload: []byte("{}"),
				})
			},
			filters: ledger.AuditFilters{SubjectID: "alice"},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newTestService()
			tt.seed(store)

			result, err := svc.Query(context.Background(), svcTenant, testTenantVis(), tt.filters, query.PageParams{})
			require.NoError(t, err)
			assert.Len(t, result.Items, tt.wantLen)
		})
	}
}

// TestService_Query_RejectsNonCanonicalTenant locks the post-auth hard boundary
// (#1618 F2): Service.Query rejects an empty or non-canonical tenant before any
// store read, so the store's empty-t = system-chain capability (a trusted
// internal-only read) can never be reached through a user-facing query. Without
// this guard an empty/garbage principal tenant would silently degrade a user
// request to the system chain instead of failing closed.
func TestService_Query_RejectsNonCanonicalTenant(t *testing.T) {
	cases := []struct {
		name string
		tid  tenant.TenantID
	}{
		{"empty tenant rejected", tenant.TenantID("")},
		{"non-canonical tenant rejected", tenant.TenantID("tenant-a")},
		{"nil-uuid tenant rejected", tenant.TenantID("00000000-0000-0000-0000-000000000000")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newTestService()
			seedEntry(store, "x-1", "event.test.v1", "usr-1", time.Now())
			_, err := svc.Query(context.Background(), tc.tid, testTenantVis(), ledger.AuditFilters{}, query.PageParams{})
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.ErrInternal, ecErr.Code,
				"a non-canonical tenant at the post-auth boundary is a server-side invariant break")
		})
	}
}

func TestService_Query_FirstPage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 5 {
		seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	result, err := svc.Query(context.Background(), svcTenant, testTenantVis(), ledger.AuditFilters{}, query.PageParams{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.True(t, result.HasMore)
	assert.NotEmpty(t, result.NextCursor)
}

func TestService_Query_WithCursor(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 10 {
		seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	page1, err := svc.Query(context.Background(), svcTenant, testTenantVis(), ledger.AuditFilters{}, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)

	page2, err := svc.Query(context.Background(), svcTenant, testTenantVis(),
		ledger.AuditFilters{}, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.NoError(t, err)
	assert.Len(t, page2.Items, 3)
	assert.NotEqual(t, page1.Items[0].ID, page2.Items[0].ID)
}

func TestService_Query_InvalidCursor(t *testing.T) {
	svc, _ := newTestService()

	_, err := svc.Query(context.Background(), svcTenant, testTenantVis(),
		ledger.AuditFilters{}, query.PageParams{Cursor: "garbage-token"})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestService_Query_LastPage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	seedEntry(store, "ae-00", "event.test.v1", "usr-1", base)
	seedEntry(store, "ae-01", "event.test.v1", "usr-1", base.Add(time.Hour))

	result, err := svc.Query(context.Background(), svcTenant, testTenantVis(), ledger.AuditFilters{}, query.PageParams{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_Query_Empty(t *testing.T) {
	svc, _ := newTestService()

	result, err := svc.Query(context.Background(), svcTenant, testTenantVis(), ledger.AuditFilters{}, query.PageParams{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_Query_CursorContextMismatch(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 5 {
		seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.login.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	loginFilters := ledger.AuditFilters{EventType: "event.login.v1"}
	page1, err := svc.Query(context.Background(), svcTenant, testTenantVis(), loginFilters, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	logoutFilters := ledger.AuditFilters{EventType: "event.logout.v1"}
	_, err = svc.Query(context.Background(), svcTenant, testTenantVis(),
		logoutFilters, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	reasonAttr, ok := ecErr.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, "query context mismatch", reasonAttr.Value().(string))
}

// TestService_Query_CursorContextMismatch_SubjectID is the subjectId sibling of
// TestService_Query_CursorContextMismatch: it locks that subjectId participates
// in the cursor-scope fingerprint (#1290, service.go QueryContext attrs). A
// cursor minted under subjectId=alice must be rejected when replayed under
// subjectId=bob (cross-context replay), exactly as eventType already is. Without
// subjectId in the fingerprint, both queries would share a QueryContext and the
// replay would silently succeed — this test would then fail.
func TestService_Query_CursorContextMismatch_SubjectID(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 5 {
		require.NoError(t, store.Append(context.Background(), &ledger.Entry{
			ID:        fmt.Sprintf("ae-%02d", i),
			EventID:   fmt.Sprintf("evt-%02d", i),
			EventType: "event.test.v1",
			ActorID:   "actor-x",
			SubjectID: "alice",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			Payload:   []byte("{}"),
		}))
	}

	alice := ledger.AuditFilters{SubjectID: "alice"}
	page1, err := svc.Query(context.Background(), svcTenant, testTenantVis(), alice, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	bob := ledger.AuditFilters{SubjectID: "bob"}
	_, err = svc.Query(context.Background(), svcTenant, testTenantVis(), bob, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	reasonAttr, ok := ecErr.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, "query context mismatch", reasonAttr.Value().(string))
}

// TestService_Query_CursorContextMismatch_TraceID is the traceId sibling of
// TestService_Query_CursorContextMismatch_SubjectID: it locks that traceId
// participates in the cursor-scope fingerprint (#1048, service.go QueryContext
// attrs). A cursor minted under TraceID="X" must be rejected when replayed under
// TraceID="Y" (cross-context replay), exactly as subjectId and eventType already
// are. Without traceId in the fingerprint both queries would share a QueryContext
// and the replay would silently succeed — this test would then fail.
func TestService_Query_CursorContextMismatch_TraceID(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 5 {
		require.NoError(t, store.Append(context.Background(), &ledger.Entry{
			ID:        fmt.Sprintf("ae-%02d", i),
			EventID:   fmt.Sprintf("evt-%02d", i),
			EventType: "event.test.v1",
			ActorID:   "actor-x",
			TraceID:   "trace-X",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			Payload:   []byte("{}"),
		}))
	}

	traceX := ledger.AuditFilters{TraceID: "trace-X"}
	page1, err := svc.Query(context.Background(), svcTenant, testTenantVis(), traceX, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	traceY := ledger.AuditFilters{TraceID: "trace-Y"}
	_, err = svc.Query(context.Background(), svcTenant, testTenantVis(),
		traceY, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	reasonAttr, ok := ecErr.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, "query context mismatch", reasonAttr.Value().(string))
}

func newTestServiceWithLogBuf() (*Service, *ledger.MemStore, *bytes.Buffer) {
	p, _ := ledger.NewProtocol(
		ledger.NamespaceID("auditcore"),
		[]byte("test-hmac-key-32bytes-long!!!!!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	store, _ := ledger.NewMemStore(p, clock.Real())
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := NewService(store, testCodec(), logger, outbox.DemoCellTxManager(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc, store, buf
}

func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec),
			"failed to parse log line: %s", line)
		out = append(out, rec)
	}
	return out
}

func TestService_Query_InvalidCursor_LogsDecode(t *testing.T) {
	svc, _, buf := newTestServiceWithLogBuf()

	badCursor := "garbage-token-should-not-appear-in-log"
	ctx := ctxkeys.WithRequestID(context.Background(), "req-test-001")
	_, err := svc.Query(ctx, svcTenant, testTenantVis(), ledger.AuditFilters{}, query.PageParams{Cursor: badCursor})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1, "expected exactly one log record")
	rec := logs[0]

	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "invalid cursor", rec["msg"])
	assert.Equal(t, "auditquery", rec["slice"])
	assert.Equal(t, "decode", rec["reason"])
	assert.Equal(t, "req-test-001", rec["request_id"])
	assert.NotEmpty(t, rec["error"])
	assert.NotContains(t, buf.String(), badCursor)
}

func TestService_Query_InvalidCursor_LogsScope(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store, buf := newTestServiceWithLogBuf()
	for i := range 5 {
		seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.login.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	loginFilters := ledger.AuditFilters{EventType: "event.login.v1"}
	page1, err := svc.Query(context.Background(), svcTenant, testTenantVis(), loginFilters, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, page1.NextCursor)

	buf.Reset()

	logoutFilters := ledger.AuditFilters{EventType: "event.logout.v1"}
	ctxWithReqID := ctxkeys.WithRequestID(context.Background(), "req-test-002")
	_, err = svc.Query(ctxWithReqID, svcTenant, testTenantVis(), logoutFilters, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1)
	rec := logs[0]

	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "invalid cursor", rec["msg"])
	assert.Equal(t, "auditquery", rec["slice"])
	assert.Equal(t, "scope", rec["reason"])
	assert.Equal(t, "req-test-002", rec["request_id"])
	assert.NotContains(t, buf.String(), page1.NextCursor)
}

func TestService_Query_InvalidCursor_NoRequestID(t *testing.T) {
	svc, _, buf := newTestServiceWithLogBuf()

	_, err := svc.Query(context.Background(), svcTenant, testTenantVis(), ledger.AuditFilters{}, query.PageParams{Cursor: "garbage"})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1)
	_, present := logs[0]["request_id"]
	assert.False(t, present, "request_id field must be absent when not in ctx")
}

// TestService_Query_CursorContextMismatch_TenantID is the tenantId sibling of
// TestService_Query_CursorContextMismatch_SubjectID: it locks that tenantId
// participates in the cursor-scope fingerprint (#1337 PR-2a review U4).
// A cursor minted under TenantID="tenant-A" must be rejected when replayed
// under TenantID="tenant-B" (cross-tenant cursor replay). Without tenantId in
// the fingerprint, both queries would share a QueryContext and the cross-tenant
// replay would silently succeed — this test would then fail.
func TestService_Query_CursorContextMismatch_TenantID(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 5 {
		require.NoError(t, store.Append(context.Background(), &ledger.Entry{
			ID:        fmt.Sprintf("ae-%02d", i),
			EventID:   fmt.Sprintf("evt-%02d", i),
			EventType: "event.test.v1",
			ActorID:   "actor-x",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			Payload:   []byte("{}"),
		}))
	}

	tenantA := tenant.TenantID("3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	page1, err := svc.Query(context.Background(), tenantA, testTenantVis(), ledger.AuditFilters{}, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	tenantB := tenant.TenantID("7c9e6679-7425-40de-944b-e07fc1f90ae7")
	_, err = svc.Query(context.Background(), tenantB, testTenantVis(),
		ledger.AuditFilters{}, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	reasonAttr, ok := ecErr.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, "query context mismatch", reasonAttr.Value().(string))
}

// TestService_Query_CursorContextMismatch_RowScope is the row-visibility sibling
// of the tenantId/subjectId/traceId mismatch tests: it locks that the row
// obligation (rowScope + rowSubject) participates in the cursor-scope fingerprint
// (#1337 PR-4, service.go QueryContext attrs). This is a security regression
// guard: a cursor minted under one obligation must be REJECTED when replayed under
// a different one, so a mid-pagination authorization change (self → tenant, or
// self(alice) → self(bob)) produces a "query context mismatch" rather than silently
// paging the wrong owner set. Without rowScope/rowSubject in the fingerprint, the
// replays would share a QueryContext and succeed — this test would then fail. It
// covers both axes the obligation contributes:
//
//   - scope change: self(alice) cursor replayed under tenant-wide scope;
//   - subject change: self(alice) cursor replayed under self(bob).
func TestService_Query_CursorContextMismatch_RowScope(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Seed 5 entries owned by alice so a self(alice) page1 returns a full page +
	// HasMore (the cursor that the replays attempt to reuse under a different
	// obligation).
	mintAlicePage1 := func(svc *Service) string {
		page1, err := svc.Query(context.Background(), svcTenant, testSelfVis("alice"),
			ledger.AuditFilters{}, query.PageParams{Limit: 3})
		require.NoError(t, err)
		require.True(t, page1.HasMore)
		require.NotEmpty(t, page1.NextCursor)
		return page1.NextCursor
	}
	assertMismatch := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
		reasonAttr, ok := ecErr.FindAttr("reason")
		require.True(t, ok)
		assert.Equal(t, "query context mismatch", reasonAttr.Value().(string))
	}

	t.Run("scope change self→tenant", func(t *testing.T) {
		svc, store := newTestService()
		for i := range 5 {
			seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.test.v1", "alice",
				base.Add(time.Duration(i)*time.Hour))
		}
		cursor := mintAlicePage1(svc)
		_, err := svc.Query(context.Background(), svcTenant, testTenantVis(),
			ledger.AuditFilters{}, query.PageParams{Limit: 3, Cursor: cursor})
		assertMismatch(t, err)
	})

	t.Run("subject change self(alice)→self(bob)", func(t *testing.T) {
		svc, store := newTestService()
		for i := range 5 {
			seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.test.v1", "alice",
				base.Add(time.Duration(i)*time.Hour))
		}
		cursor := mintAlicePage1(svc)
		_, err := svc.Query(context.Background(), svcTenant, testSelfVis("bob"),
			ledger.AuditFilters{}, query.PageParams{Limit: 3, Cursor: cursor})
		assertMismatch(t, err)
	})
}

// TestService_Query_SubsecondFilterContext verifies that From/To are not part of
// the cursor scope fingerprint. Changing From between pages does not invalidate
// the cursor — time-range filters narrow store results but do not define the
// "kind of data" being paged (F-07: zero and non-zero From are both excluded
// from scope to prevent "0001-01-01T00:00:00Z" noise and to allow callers to
// refine time windows across page requests without cursor invalidation).
func TestService_Query_SubsecondFilterContext(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 10 {
		seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Millisecond))
	}

	filtersA := ledger.AuditFilters{From: base.Add(auditNs100)}
	pageA, err := svc.Query(context.Background(), svcTenant, testTenantVis(), filtersA, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, pageA.HasMore)

	// From/To are NOT cursor-scope keys — changing From must not invalidate the cursor.
	filtersB := ledger.AuditFilters{From: base.Add(auditNs200)}
	_, err = svc.Query(context.Background(), svcTenant, testTenantVis(), filtersB, query.PageParams{
		Limit:  3,
		Cursor: pageA.NextCursor,
	})
	require.NoError(t, err, "changing From between pages must not invalidate the cursor")
}

// --- QueryCrossTenant tests (#1810) ---

// fakeCtStore is a minimal in-process CrossTenantQueryStore for unit tests.
// It returns a fixed slice of entries without needing a real admin pool.
type fakeCtStore struct {
	entries []*ledger.Entry
	err     error
}

func (f *fakeCtStore) QueryCrossTenant(
	_ context.Context, _ tenant.CrossTenantVisibility,
	_ ledger.AuditFilters, params query.ListParams,
) ([]*ledger.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	limit := params.FetchLimit()
	if limit > len(f.entries) {
		limit = len(f.entries)
	}
	return f.entries[:limit], nil
}

// TestService_QueryCrossTenant_NilStore_Returns501 locks the fail-closed
// optionality contract (#1810): when crossTenantStore is nil (admin pool not
// provisioned), QueryCrossTenant must return RowScopeAllUnsupportedError (501),
// never nil/empty — the pre-#1810 behavior is preserved.
func TestService_QueryCrossTenant_NilStore_Returns501(t *testing.T) {
	svc, _ := newTestService() // no WithCrossTenantStore → crossTenantStore is nil

	ctv := tenant.NewCrossTenantVisibility()
	_, err := svc.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{}, query.PageParams{})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindNotImplemented, ecErr.Kind,
		"nil crossTenantStore must return RowScopeAllUnsupportedError (501)")
}

// TestService_QueryCrossTenant_WithStore_ReturnsPaged verifies that when a
// CrossTenantQueryStore is wired, QueryCrossTenant returns its results via the
// standard ExecutePagedQuery machinery (#1810), including hasMore detection and
// cursor generation when more entries exist than the requested page limit.
//
// The fake seeds 4 entries; limit=2 means FetchLimit()=3, so the fake returns 3
// rows. ExecutePagedQuery detects len(rows)>limit → HasMore=true, NextCursor non-empty.
// Without this test, the N+1 hasMore path for cross-tenant reads was never asserted.
func TestService_QueryCrossTenant_WithStore_ReturnsPaged(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []*ledger.Entry{
		{
			ID: "ct-1", EventID: "evt-ct-1", EventType: "vis.v1",
			ActorID: "sa", TenantID: auditQueryTestTenant,
			Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "ct-2", EventID: "evt-ct-2", EventType: "vis.v1",
			ActorID: "sa", TenantID: auditQueryTestTenantB,
			Timestamp: base.Add(time.Second), Payload: []byte("{}"),
		},
		{
			ID: "ct-3", EventID: "evt-ct-3", EventType: "vis.v1",
			ActorID: "sa", TenantID: auditQueryTestTenant,
			Timestamp: base.Add(2 * time.Second), Payload: []byte("{}"),
		},
		{
			ID: "ct-4", EventID: "evt-ct-4", EventType: "vis.v1",
			ActorID: "sa", TenantID: auditQueryTestTenantB,
			Timestamp: base.Add(3 * time.Second), Payload: []byte("{}"),
		},
	}
	fake := &fakeCtStore{entries: entries}

	store := newTestStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(fake))
	require.NoError(t, err)

	ctv := tenant.NewCrossTenantVisibility()

	// First page: limit=2, fake has 4 entries → FetchLimit()=3, fake returns 3 rows
	// → hasMore detection fires (len(3) > limit(2)).
	result, err := svc.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{}, query.PageParams{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2, "page-1 must contain exactly 2 items (page limit)")
	assert.True(t, result.HasMore, "HasMore must be true when store has more entries than limit")
	assert.NotEmpty(t, result.NextCursor, "NextCursor must be non-empty when HasMore is true")
}

// TestQuery_ZeroTime_SkipsFromToFormat asserts that when filters.From and filters.To
// are zero, the QueryContext attrs slice does NOT contain "from" or "to" keys.
//
// A-07 RED: current implementation always calls filters.From.Format(time.RFC3339Nano)
// which formats zero time as "0001-01-01T00:00:00Z" and includes it as a "from" key
// in the cursor scope fingerprint.
//
// Observable: if zero time is formatted and embedded in cursor scope, then a cursor
// obtained with zero-UTC From and a cursor obtained with zero-non-UTC From would have
// different scope fingerprints (different Format output for different timezones),
// causing a cursor-context mismatch error on page 2.
// In GREEN state (zero time omitted), both produce identical scopes → no mismatch.
//
// We simulate this by obtaining page1 cursor using zero UTC time (time.Time{})
// and then using the same cursor with an equivalent zero time in a fixed timezone
// (time.Time{}.In(time.UTC) is same, so we use the second query with explicit
// non-UTC zero). Actually both format to the same if zone is same — so instead we
// directly confirm that page1→page2 succeeds, then assert that changing From to a
// non-zero value causes scope mismatch (proving "from" IS in scope in RED state).
//
// RED observable: in current code, query scope includes "from=0001-01-01T00:00:00Z".
// A non-zero From on page2 will cause scope mismatch → cursor invalid error.
// GREEN: "from" is not in scope → changing From to non-zero still mismatches because
// actorId/eventType are checked, but From absent means the scope is identical regardless.
// We assert: page2 with From=non-zero returns scope-mismatch error in RED state,
// and page2 with From=non-zero returns NO error in GREEN state (from not in scope).
func TestQuery_ZeroTime_SkipsFromToFormat(t *testing.T) {
	p, err := ledger.NewProtocol(
		ledger.NamespaceID("auditcore"),
		[]byte("test-hmac-key-32bytes-long!!!!!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		seedEntry(store, fmt.Sprintf("zt-%d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	// Page 1: zero From/To (no time filter).
	zeroFilters := ledger.AuditFilters{} // From and To are zero value
	page1, err := svc.Query(context.Background(), svcTenant, testTenantVis(), zeroFilters, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)

	// Page 2 attempt: same cursor, but now pass a non-zero From.
	// If zero time is embedded in cursor scope (RED state), "from" changes from
	// "0001-01-01T00:00:00Z" to a real timestamp → scope mismatch → error.
	// If zero time is NOT embedded (GREEN state), "from" was absent → non-zero
	// From IS a scope change → still mismatch. Hmm, same result.
	//
	// Better approach: page1 with zero From, page2 with same cursor and ALSO zero From.
	// Must succeed. Then assert the query context for page1 does NOT include
	// "0001-01-01" anywhere in the cursor token (cursor is base64/encrypted, can't check directly).
	//
	// Simplest reliable RED observable: count the scope keys by comparing what
	// cursor from (zeroFrom, nonzeroActorId) page vs (zeroFrom, sameActorId) page.
	// Alternatively: verify page1 cursor is reusable with page2 zero-From (passes now)
	// AND that the service's QueryContext does not embed "0001-01-01" by checking
	// the invalid-cursor log when we deliberately break the scope.
	//
	// Final approach: page1 zero-From, then page2 zero-From with mismatched eventType.
	// In both RED and GREEN, this causes scope mismatch. Not useful.
	//
	// Correct RED-only observable: a page1 obtained with zero From/To, then a page2
	// obtained with From=base (non-zero). If "from" IS in scope (RED), page2 gets
	// a scope-mismatch error. If "from" is NOT in scope (GREEN), From can change
	// freely without breaking the cursor → page2 succeeds normally.
	nonZeroFromFilters := ledger.AuditFilters{From: base}
	_, err2 := svc.Query(context.Background(), svcTenant, testTenantVis(), nonZeroFromFilters, query.PageParams{
		Limit:  3,
		Cursor: page1.NextCursor,
	})
	// A-07 RED: err2 is non-nil (scope mismatch) because "from" IS embedded in
	// cursor scope with value "0001-01-01T00:00:00Z" ≠ base.Format(RFC3339Nano).
	// A-07 GREEN: err2 is nil because "from" is NOT in cursor scope, so changing
	// From from zero to non-zero does not break the cursor.
	if err2 == nil {
		// GREEN: "from" not in scope, changing From didn't break cursor → PASS
		t.Logf("TestQuery_ZeroTime_SkipsFromToFormat: GREEN — 'from' not in scope (cursor reusable across From change)")
	} else {
		// RED: scope mismatch because "from=0001-01-01T00:00:00Z" was embedded
		var ecErr *errcode.Error
		if errors.As(err2, &ecErr) && ecErr.Code == errcode.ErrCursorInvalid {
			t.Errorf("A-07 RED: cursor scope mismatch when changing From zero→nonzero; " +
				"'from' is embedded in QueryContext for zero time. Fix: skip From/To when zero.")
		} else {
			t.Errorf("unexpected error on page2 with non-zero From: %v", err2)
		}
	}
}

// --- F2: QueryCrossTenant PEP obligation validation (Codex review #2051) ---

// TestService_QueryCrossTenant_ZeroValue_FailsClosed locks F2 (Codex review):
// a zero-value tenant.CrossTenantVisibility{} carries an invalid/zero RowVisibility;
// QueryCrossTenant must fail-closed (KindInternal) rather than forwarding an invalid
// obligation to the cross-tenant store. The typed funnel is Hard (forget/forge = compile
// error), but the zero value is constructable — this PEP guard closes the residual gap.
//
// Without the fix this test would panic (nil-pointer) or return empty results from the
// store rather than an error.
func TestService_QueryCrossTenant_ZeroValue_FailsClosed(t *testing.T) {
	store := newTestStore(t)
	fake := &fakeCtStore{} // would return entries if reached

	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(fake))
	require.NoError(t, err)

	// Zero-value CrossTenantVisibility: no NewCrossTenantVisibility() call.
	var zeroCTV tenant.CrossTenantVisibility

	_, err = svc.QueryCrossTenant(context.Background(), zeroCTV, ledger.AuditFilters{}, query.PageParams{})
	require.Error(t, err, "zero CrossTenantVisibility must fail-closed")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInternal, ecErr.Kind,
		"zero CrossTenantVisibility must yield KindInternal (PEP fail-closed), got kind=%v", ecErr.Kind)
}

// --- F5: WithCrossTenantStore typed-nil bypass (Codex review #2051) ---

// TestWithCrossTenantStore_TypedNil_KeepsStoreNil locks F5 (Codex review):
// a typed-nil (*MemCrossTenantStore)(nil) is != nil at the interface level, so a plain
// `s == nil` check would store it and bypass the 501 fail-closed path (the subsequent
// nil-pointer call to QueryCrossTenant would panic). WithCrossTenantStore must use
// validation.IsNilInterface to detect typed-nil values.
//
// Without the fix, QueryCrossTenant would panic (nil method call on typed-nil interface)
// instead of returning RowScopeAllUnsupportedError (501).
func TestWithCrossTenantStore_TypedNil_KeepsStoreNil(t *testing.T) {
	store := newTestStore(t)

	// Inject a typed-nil: interface value is non-nil (has a concrete type), but
	// the underlying pointer is nil — triggers nil-panic without IsNilInterface.
	var typedNilStore *ledger.MemCrossTenantStore

	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(typedNilStore))
	require.NoError(t, err, "NewService must succeed even with typed-nil CrossTenantStore")

	// The typed-nil must NOT have been stored: QueryCrossTenant must return 501
	// (RowScopeAllUnsupportedError), not panic.
	ctv := tenant.NewCrossTenantVisibility()
	_, err = svc.QueryCrossTenant(context.Background(), ctv, ledger.AuditFilters{}, query.PageParams{})
	require.Error(t, err, "QueryCrossTenant must fail-closed when typed-nil store was injected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindNotImplemented, ecErr.Kind,
		"typed-nil store must yield RowScopeAllUnsupportedError (501), not a nil-pointer panic")
}
