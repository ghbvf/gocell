package auditquery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

func newTestService() (*Service, *mem.AuditRepository) {
	repo := mem.NewAuditRepository()
	svc, err := NewService(repo, testCodec(), slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc, repo
}

func TestNewService_NilCodec_ReturnsError(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc, err := NewService(repo, nil, slog.Default(), query.RunModeProd)
	require.Error(t, err)
	assert.Nil(t, svc)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
}

func seedEntry(repo *mem.AuditRepository, id, eventType, actorID string, ts time.Time) {
	_ = repo.Append(context.Background(), &domain.AuditEntry{
		ID:        id,
		EventID:   "evt-" + id,
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: ts,
		Payload:   []byte("{}"),
	})
}

func TestService_Query(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		seed    func(*mem.AuditRepository)
		filters ports.AuditFilters
		wantLen int
	}{
		{
			name:    "empty repository",
			seed:    func(_ *mem.AuditRepository) {},
			filters: ports.AuditFilters{},
			wantLen: 0,
		},
		{
			name: "all entries",
			seed: func(r *mem.AuditRepository) {
				seedEntry(r, "a-1", "event.user.created.v1", "usr-1", now)
				seedEntry(r, "a-2", "event.session.created.v1", "usr-1", now.Add(time.Second))
			},
			filters: ports.AuditFilters{},
			wantLen: 2,
		},
		{
			name: "filter by event type",
			seed: func(r *mem.AuditRepository) {
				seedEntry(r, "a-1", "event.user.created.v1", "usr-1", now)
				seedEntry(r, "a-2", "event.session.created.v1", "usr-2", now.Add(time.Second))
			},
			filters: ports.AuditFilters{EventType: "event.user.created.v1"},
			wantLen: 1,
		},
		{
			name: "filter by actor",
			seed: func(r *mem.AuditRepository) {
				seedEntry(r, "a-1", "event.user.created.v1", "usr-1", now)
				seedEntry(r, "a-2", "event.user.created.v1", "usr-2", now.Add(time.Second))
			},
			filters: ports.AuditFilters{ActorID: "usr-1"},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			tt.seed(repo)

			result, err := svc.Query(context.Background(), tt.filters, query.PageRequest{})
			require.NoError(t, err)
			assert.Len(t, result.Items, tt.wantLen)
		})
	}
}

func TestService_Query_FirstPage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, repo := newTestService()
	for i := 0; i < 5; i++ {
		seedEntry(repo, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	result, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.True(t, result.HasMore)
	assert.NotEmpty(t, result.NextCursor)
	// DESC: newest first
	assert.Equal(t, "ae-04", result.Items[0].ID)
}

func TestService_Query_WithCursor(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, repo := newTestService()
	for i := 0; i < 10; i++ {
		seedEntry(repo, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	// Get first page
	page1, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)

	// Get second page using cursor
	page2, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{Limit: 3, Cursor: page1.NextCursor})
	require.NoError(t, err)
	assert.Len(t, page2.Items, 3)
	// Second page should continue where first left off
	assert.NotEqual(t, page1.Items[0].ID, page2.Items[0].ID)
}

func TestService_Query_InvalidCursor(t *testing.T) {
	svc, _ := newTestService()

	_, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{Cursor: "garbage-token"})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestService_Query_LastPage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, repo := newTestService()
	seedEntry(repo, "ae-00", "event.test.v1", "usr-1", base)
	seedEntry(repo, "ae-01", "event.test.v1", "usr-1", base.Add(time.Hour))

	result, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_Query_Empty(t *testing.T) {
	svc, _ := newTestService()

	result, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_Query_CursorContextMismatch(t *testing.T) {
	// Create a cursor with eventType=login context, then query with eventType=logout.
	// The cursor should be rejected.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, repo := newTestService()
	for i := 0; i < 5; i++ {
		seedEntry(repo, fmt.Sprintf("ae-%02d", i), "event.login.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	// Get first page with eventType=login filter.
	loginFilters := ports.AuditFilters{EventType: "event.login.v1"}
	page1, err := svc.Query(context.Background(), loginFilters, query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	// Replay the cursor with a different eventType filter — must fail.
	logoutFilters := ports.AuditFilters{EventType: "event.logout.v1"}
	_, err = svc.Query(context.Background(), logoutFilters, query.PageRequest{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "query context mismatch", ecErr.Details["reason"])
}

// newTestServiceWithLogBuf returns a Service wired to a JSON-capturing logger
// and the underlying buffer for post-call inspection.
func newTestServiceWithLogBuf() (*Service, *mem.AuditRepository, *bytes.Buffer) {
	repo := mem.NewAuditRepository()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := NewService(repo, testCodec(), logger, query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc, repo, buf
}

// parseLogLines parses each non-empty newline-delimited JSON record in buf.
func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
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

// TestService_Query_InvalidCursor_LogsDecode verifies that a malformed cursor
// produces a structured Info log line with reason=decode, slice=auditquery,
// and request_id propagated from ctx, without leaking the raw cursor string
// (aligned with k8s/etcd/MinIO).
func TestService_Query_InvalidCursor_LogsDecode(t *testing.T) {
	svc, _, buf := newTestServiceWithLogBuf()

	badCursor := "garbage-token-should-not-appear-in-log"
	ctx := ctxkeys.WithRequestID(context.Background(), "req-test-001")
	_, err := svc.Query(ctx, ports.AuditFilters{}, query.PageRequest{Cursor: badCursor})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1, "expected exactly one log record")
	rec := logs[0]

	assert.Equal(t, "INFO", rec["level"], "level must be INFO (client input error, not server degradation)")
	assert.Equal(t, "invalid cursor", rec["msg"])
	assert.Equal(t, "auditquery", rec["slice"])
	assert.Equal(t, "decode", rec["reason"])
	assert.Equal(t, "req-test-001", rec["request_id"])
	assert.NotEmpty(t, rec["error"])
	assert.NotContains(t, buf.String(), badCursor,
		"raw cursor string must not appear in log output")
}

// TestService_Query_InvalidCursor_LogsScope verifies that a context-mismatched
// cursor produces a log line with reason=scope.
func TestService_Query_InvalidCursor_LogsScope(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, repo, buf := newTestServiceWithLogBuf()
	for i := range 5 {
		seedEntry(repo, fmt.Sprintf("ae-%02d", i), "event.login.v1", "usr-1",
			base.Add(time.Duration(i)*time.Hour))
	}

	// Issue a valid first page to mint a scoped cursor.
	loginFilters := ports.AuditFilters{EventType: "event.login.v1"}
	page1, err := svc.Query(context.Background(), loginFilters, query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, page1.NextCursor)

	// Reset log buffer so the scope-mismatch assertion sees only the next call.
	buf.Reset()

	// Replay the cursor under a mismatched scope — with a request_id in ctx
	// to confirm correlation propagation.
	logoutFilters := ports.AuditFilters{EventType: "event.logout.v1"}
	ctxWithReqID := ctxkeys.WithRequestID(context.Background(), "req-test-002")
	_, err = svc.Query(ctxWithReqID, logoutFilters, query.PageRequest{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1, "expected exactly one log record")
	rec := logs[0]

	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "invalid cursor", rec["msg"])
	assert.Equal(t, "auditquery", rec["slice"])
	assert.Equal(t, "scope", rec["reason"])
	assert.Equal(t, "req-test-002", rec["request_id"])
	assert.NotEmpty(t, rec["error"])
	assert.NotContains(t, buf.String(), page1.NextCursor,
		"raw cursor string must not appear in log output")
}

// TestService_Query_InvalidCursor_NoRequestID verifies the log record omits
// request_id when no ID is present in ctx (field is conditional, never "").
func TestService_Query_InvalidCursor_NoRequestID(t *testing.T) {
	svc, _, buf := newTestServiceWithLogBuf()

	_, err := svc.Query(context.Background(), ports.AuditFilters{}, query.PageRequest{Cursor: "garbage"})
	require.Error(t, err)

	logs := parseLogLines(t, buf)
	require.Len(t, logs, 1)
	_, present := logs[0]["request_id"]
	assert.False(t, present, "request_id field must be absent when not in ctx")
}

func TestService_Query_SubsecondFilterContext(t *testing.T) {
	// Two queries with from/to at the same second but different nanoseconds
	// must produce different QueryContext fingerprints, so a cursor from one
	// is rejected by the other.
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc, repo := newTestService()
	for i := range 10 {
		seedEntry(repo, fmt.Sprintf("ae-%02d", i), "event.test.v1", "usr-1",
			base.Add(time.Duration(i)*time.Millisecond))
	}

	// Query A: from = base+100ns
	filtersA := ports.AuditFilters{From: base.Add(100 * time.Nanosecond)}
	pageA, err := svc.Query(context.Background(), filtersA, query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.True(t, pageA.HasMore)

	// Query B: from = base+200ns (same second, different nanosecond)
	filtersB := ports.AuditFilters{From: base.Add(200 * time.Nanosecond)}
	_, err = svc.Query(context.Background(), filtersB, query.PageRequest{
		Limit:  3,
		Cursor: pageA.NextCursor,
	})
	require.Error(t, err, "cursor from query A must be rejected by query B with different sub-second from filter")
	var ecErr2 *errcode.Error
	require.ErrorAs(t, err, &ecErr2)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr2.Code)
}
