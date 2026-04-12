package auditquery

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
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
	return NewService(repo, testCodec(), slog.Default()), repo
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
