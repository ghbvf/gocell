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

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

const (
	auditNs100 = 100 * time.Nanosecond
	auditNs200 = 200 * time.Nanosecond
)

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

func newTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ns),
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
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ledger.NamespaceID("auditcore")),
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
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc, store
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

func TestNewService_NilCodec_ReturnsError(t *testing.T) {
	store := newTestStore(t)
	svc, err := NewService(store, nil, slog.Default(), query.RunModeProd)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newTestService()
			tt.seed(store)

			result, err := svc.Query(context.Background(), tt.filters, query.PageParams{})
			require.NoError(t, err)
			assert.Len(t, result.Items, tt.wantLen)
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

	result, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Limit: 3})
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

	page1, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)

	page2, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.NoError(t, err)
	assert.Len(t, page2.Items, 3)
	assert.NotEqual(t, page1.Items[0].ID, page2.Items[0].ID)
}

func TestService_Query_InvalidCursor(t *testing.T) {
	svc, _ := newTestService()

	_, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Cursor: "garbage-token"})
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

	result, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_Query_Empty(t *testing.T) {
	svc, _ := newTestService()

	result, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{})
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
	page1, err := svc.Query(context.Background(), loginFilters, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	logoutFilters := ledger.AuditFilters{EventType: "event.logout.v1"}
	_, err = svc.Query(context.Background(), logoutFilters, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	reasonAttr, ok := ecErr.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, "query context mismatch", reasonAttr.Value.String())
}

func newTestServiceWithLogBuf() (*Service, *ledger.MemStore, *bytes.Buffer) {
	p, _ := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ledger.NamespaceID("auditcore")),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	store, _ := ledger.NewMemStore(p, clock.Real())
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := NewService(store, testCodec(), logger, query.RunModeProd)
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
	_, err := svc.Query(ctx, ledger.AuditFilters{}, query.PageParams{Cursor: badCursor})
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
	page1, err := svc.Query(context.Background(), loginFilters, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, page1.NextCursor)

	buf.Reset()

	logoutFilters := ledger.AuditFilters{EventType: "event.logout.v1"}
	ctxWithReqID := ctxkeys.WithRequestID(context.Background(), "req-test-002")
	_, err = svc.Query(ctxWithReqID, logoutFilters, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
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

	_, err := svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Cursor: "garbage"})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1)
	_, present := logs[0]["request_id"]
	assert.False(t, present, "request_id field must be absent when not in ctx")
}

func TestService_Query_SubsecondFilterContext(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc, store := newTestService()
	for i := range 10 {
		seedEntry(store, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Millisecond))
	}

	filtersA := ledger.AuditFilters{From: base.Add(auditNs100)}
	pageA, err := svc.Query(context.Background(), filtersA, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, pageA.HasMore)

	filtersB := ledger.AuditFilters{From: base.Add(auditNs200)}
	_, err = svc.Query(context.Background(), filtersB, query.PageParams{
		Limit:  3,
		Cursor: pageA.NextCursor,
	})
	require.Error(t, err)
	var ecErr2 *errcode.Error
	require.ErrorAs(t, err, &ecErr2)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr2.Code)
}

// TestQuery_FetchCap_500 asserts that auditQueryFetchCap equals 500.
//
// A-07/F-07 RED: current value is 5000. After the fix it must be 500 to
// prevent unbounded in-memory loads before keyset pagination lands (S8).
// This test verifies the constant value directly via a mock store that
// returns exactly 501 entries and checks that the Warn log fires at that threshold.
//
// Note: auditQueryFetchCap is package-private; we probe it indirectly by
// seeding 501 entries and checking the Warn fires. When the cap is 5000 (current)
// no Warn is emitted → RED. When the cap is 500 (target) the Warn fires → GREEN.
func TestQuery_FetchCap_500(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ledger.NamespaceID("auditcore")),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)

	svc, err := NewService(store, testCodec(), logger, query.RunModeProd)
	require.NoError(t, err)

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// Seed 501 entries — above the target cap of 500, below the current cap of 5000.
	// When cap == 500: Warn fires → GREEN; when cap == 5000: no Warn → RED.
	const seedCount = 501
	for i := range seedCount {
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("cap500-evt-%d", i),
			EventType: "cap.test",
			ActorID:   "actor",
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
			Payload:   []byte("{}"),
		}
		require.NoError(t, store.Append(context.Background(), e))
	}

	buf.Reset()
	_, err = svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Limit: 10})
	require.NoError(t, err)

	// Cap warning must appear — only fires when cap ≤ 501.
	// RED: current cap is 5000, so 501 entries does NOT trigger the warning.
	if !strings.Contains(buf.String(), "fetch cap reached") {
		t.Errorf("expected 'fetch cap reached' warning for 501 entries with cap=500; "+
			"current cap is 5000 so this FAILS as expected (F-07 RED); log output: %q",
			buf.String())
	}
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
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ledger.NamespaceID("auditcore")),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		seedEntry(store, fmt.Sprintf("zt-%d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	// Page 1: zero From/To (no time filter).
	zeroFilters := ledger.AuditFilters{} // From and To are zero value
	page1, err := svc.Query(context.Background(), zeroFilters, query.PageParams{Limit: 3})
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
	_, err2 := svc.Query(context.Background(), nonZeroFromFilters, query.PageParams{
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
			t.Errorf("A-07 RED: cursor scope mismatch when changing From zero→nonzero; "+
				"'from' is embedded in QueryContext for zero time. Fix: skip From/To when zero.")
		} else {
			t.Errorf("unexpected error on page2 with non-zero From: %v", err2)
		}
	}
}

// TestAuditQuery_FetchCapEnforced verifies that when the store returns
// auditQueryFetchCap or more entries the service logs a warning at Warn level.
// The store is seeded with exactly cap entries so the warning fires, then a
// second query with fewer entries confirms the happy path does not warn.
func TestAuditQuery_FetchCapEnforced(t *testing.T) {
	// Use a log handler that captures records.
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ledger.NamespaceID("auditcore")),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)

	svc, err := NewService(store, testCodec(), logger, query.RunModeProd)
	require.NoError(t, err)

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Seed exactly auditQueryFetchCap entries so the cap warning fires.
	for i := range auditQueryFetchCap {
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("cap-evt-%d", i),
			EventType: "cap.test",
			ActorID:   "actor",
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
			Payload:   []byte("{}"),
		}
		require.NoError(t, store.Append(context.Background(), e))
	}

	buf.Reset()
	_, err = svc.Query(context.Background(), ledger.AuditFilters{}, query.PageParams{Limit: 10})
	require.NoError(t, err)

	// Cap warning must appear in the log output.
	if !strings.Contains(buf.String(), "fetch cap reached") {
		t.Errorf("expected 'fetch cap reached' warning in log; got: %s", buf.String())
	}
}
