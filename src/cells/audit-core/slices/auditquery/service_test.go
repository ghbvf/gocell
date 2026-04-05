package auditquery

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.AuditRepository) {
	repo := mem.NewAuditRepository()
	return NewService(repo, slog.Default()), repo
}

func seedEntry(repo *mem.AuditRepository, eventType, actorID string) {
	_ = repo.Append(context.Background(), &domain.AuditEntry{
		ID:        "audit-" + eventType,
		EventID:   "evt-1",
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: time.Now(),
		Payload:   []byte("{}"),
	})
}

func TestService_Query(t *testing.T) {
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
				seedEntry(r, "event.user.created.v1", "usr-1")
				seedEntry(r, "event.session.created.v1", "usr-1")
			},
			filters: ports.AuditFilters{},
			wantLen: 2,
		},
		{
			name: "filter by event type",
			seed: func(r *mem.AuditRepository) {
				seedEntry(r, "event.user.created.v1", "usr-1")
				seedEntry(r, "event.session.created.v1", "usr-2")
			},
			filters: ports.AuditFilters{EventType: "event.user.created.v1"},
			wantLen: 1,
		},
		{
			name: "filter by actor",
			seed: func(r *mem.AuditRepository) {
				seedEntry(r, "event.user.created.v1", "usr-1")
				seedEntry(r, "event.user.created.v1", "usr-2")
			},
			filters: ports.AuditFilters{ActorID: "usr-1"},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			tt.seed(repo)

			entries, err := svc.Query(context.Background(), tt.filters)
			require.NoError(t, err)
			assert.Len(t, entries, tt.wantLen)
		})
	}
}
