package mem

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestAuditRepository_Append_And_Len(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	assert.Equal(t, 0, repo.Len())

	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-1", EventType: "login", ActorID: "usr-1",
		Timestamp: time.Now(),
	}))
	assert.Equal(t, 1, repo.Len())
}

func TestAuditRepository_Append_StoresCopy(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	entry := &domain.AuditEntry{
		ID: "ae-copy", EventType: "login", ActorID: "usr-1",
		Timestamp: time.Now(),
	}
	require.NoError(t, repo.Append(ctx, entry))

	entry.EventType = "mutated"
	got, err := repo.GetRange(ctx, 0, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "login", got[0].EventType, "stored entry should be a copy")
}

func TestAuditRepository_GetRange(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
			ID: fmt.Sprintf("ae-%d", i), EventType: "evt",
			Timestamp: time.Now(),
		}))
	}

	tests := []struct {
		name    string
		from    int
		to      int
		wantLen int
	}{
		{name: "full range", from: 0, to: 5, wantLen: 5},
		{name: "partial range", from: 1, to: 3, wantLen: 2},
		{name: "negative from clamped", from: -1, to: 2, wantLen: 2},
		{name: "to exceeds length", from: 3, to: 100, wantLen: 2},
		{name: "from >= to returns nil", from: 3, to: 2, wantLen: 0},
		{name: "empty range", from: 5, to: 5, wantLen: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.GetRange(ctx, tc.from, tc.to)
			require.NoError(t, err)
			assert.Len(t, got, tc.wantLen)
		})
	}
}

func TestAuditRepository_Query_Sort_ByEventType(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []struct {
		id        string
		eventType string
	}{
		{"ae-1", "config.changed"},
		{"ae-2", "audit.archived"},
		{"ae-3", "session.created"},
	}
	for i, e := range entries {
		require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
			ID: e.id, EventType: e.eventType, ActorID: "usr-1",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		}))
	}

	// The audit repo query sorts inline with timestamp/id only.
	// But we can test the eventType filter path.
	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{EventType: "session.created"}, params)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "session.created", result[0].EventType)
}

func TestAuditRepository_Query_Sort_ByActorID(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-1", EventType: "login", ActorID: "usr-alice",
		Timestamp: base,
	}))
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-2", EventType: "login", ActorID: "usr-bob",
		Timestamp: base.Add(time.Hour),
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{ActorID: "usr-bob"}, params)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "usr-bob", result[0].ActorID)
}

func TestAuditRepository_Query_Filter_TimeRange(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
			ID:        fmt.Sprintf("ae-%d", i),
			EventType: "login",
			ActorID:   "usr-1",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		}))
	}

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}

	// Filter from hour 1 to hour 3 (inclusive boundaries are Before/After)
	filters := ports.AuditFilters{
		From: base.Add(1 * time.Hour),
		To:   base.Add(3 * time.Hour),
	}
	result, err := repo.Query(ctx, filters, params)
	require.NoError(t, err)
	// From filters out entries Before(from), To filters out entries After(to).
	// ae-0 (hour 0) is Before from -> excluded
	// ae-1 (hour 1) is NOT Before from -> included
	// ae-2 (hour 2) -> included
	// ae-3 (hour 3) is NOT After to -> included
	// ae-4 (hour 4) is After to -> excluded
	assert.Len(t, result, 3)
}

func TestAuditRepository_Query_Sort_DESC(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
			ID:        fmt.Sprintf("ae-%02d", i),
			EventType: "login",
			ActorID:   "usr-1",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		}))
	}

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	require.Len(t, result, 3)
	// DESC: newest first
	assert.Equal(t, "ae-02", result[0].ID)
	assert.Equal(t, "ae-01", result[1].ID)
	assert.Equal(t, "ae-00", result[2].ID)
}

func TestAuditRepository_Query_Cursor(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
			ID:        fmt.Sprintf("ae-%02d", i),
			EventType: "login",
			ActorID:   "usr-1",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		}))
	}

	// Sort DESC, cursor after ae-03 (timestamp = base+3h)
	cursorTS := base.Add(3 * time.Hour).Format("2006-01-02T15:04:05.999999999Z07:00")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "ae-03"},
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	// After ae-03 in DESC order: ae-02, ae-01, ae-00
	require.Len(t, result, 3)
	assert.Equal(t, "ae-02", result[0].ID)
	assert.Equal(t, "ae-01", result[1].ID)
	assert.Equal(t, "ae-00", result[2].ID)
}

func TestAuditRepository_Query_Cursor_PastEnd(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-01", EventType: "login", ActorID: "usr-1",
		Timestamp: base,
	}))

	// Cursor with timestamp far in the past -> everything in DESC order is before cursor
	cursorTS := base.Add(-24 * time.Hour).Format("2006-01-02T15:04:05.999999999Z07:00")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "ae-01"},
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestAuditRepository_Query_NoSort(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-1", EventType: "login", ActorID: "usr-1",
		Timestamp: time.Now(),
	}))

	// No sort columns -> preserves insertion order
	params := query.ListParams{Limit: 10}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	assert.Len(t, result, 1)
}

func TestAuditRepository_Query_Limit(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
			ID: fmt.Sprintf("ae-%d", i), EventType: "login",
			Timestamp: time.Now(),
		}))
	}

	params := query.ListParams{
		Limit: 2,
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	// FetchLimit = 2+1 = 3
	assert.Len(t, result, 3)
}

func TestAuditRepository_Query_UnknownSortField(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-1", EventType: "login", ActorID: "usr-1",
		Timestamp: time.Now(),
	}))
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-2", EventType: "logout", ActorID: "usr-2",
		Timestamp: time.Now(),
	}))

	// Unknown sort field results in comparison returning 0 (stable sort)
	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "unknown_field", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestAuditRepository_Query_Cursor_SubsecondPrecision(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	// Two entries at the same second but different nanoseconds.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-01", EventType: "login", ActorID: "usr-1",
		Timestamp: base.Add(100 * time.Nanosecond),
	}))
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-02", EventType: "login", ActorID: "usr-1",
		Timestamp: base.Add(200 * time.Nanosecond),
	}))
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-03", EventType: "login", ActorID: "usr-1",
		Timestamp: base.Add(300 * time.Nanosecond),
	}))

	// Sort DESC by timestamp, cursor at ae-02.
	cursorTS := base.Add(200 * time.Nanosecond).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "ae-02"},
		Sort: []query.SortColumn{
			{Name: "timestamp", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	// After ae-02 in DESC order: ae-01 (100ns)
	require.Len(t, result, 1)
	assert.Equal(t, "ae-01", result[0].ID)
}

func TestAuditRepository_Query_SortByID(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-z", EventType: "login", Timestamp: base,
	}))
	require.NoError(t, repo.Append(ctx, &domain.AuditEntry{
		ID: "ae-a", EventType: "login", Timestamp: base,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.Query(ctx, ports.AuditFilters{}, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "ae-a", result[0].ID)
	assert.Equal(t, "ae-z", result[1].ID)
}

// TestAuditRepository_ConcurrentAppendAndQuery verifies that concurrent
// Append and Query calls do not race. Run with -race to verify.
func TestAuditRepository_ConcurrentAppendAndQuery(t *testing.T) {
	repo := NewAuditRepository()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const writers = 5
	const readers = 10
	const iterations = 50

	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				_ = repo.Append(ctx, &domain.AuditEntry{
					ID:        fmt.Sprintf("ae-w%d-i%d", id, i),
					EventType: "login",
					ActorID:   fmt.Sprintf("usr-%d", id),
					Timestamp: base.Add(time.Duration(id*iterations+i) * time.Millisecond),
				})
			}
		}(w)
	}

	var readErrors atomic.Int64
	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			params := query.ListParams{
				Limit: 10,
				Sort: []query.SortColumn{
					{Name: "timestamp", Direction: query.SortDESC},
					{Name: "id", Direction: query.SortASC},
				},
			}
			for range iterations {
				items, err := repo.Query(ctx, ports.AuditFilters{}, params)
				if err != nil {
					readErrors.Add(1)
					continue
				}
				// Semantic invariant: results must be DESC-sorted by timestamp.
				for j := 1; j < len(items); j++ {
					if items[j].Timestamp.After(items[j-1].Timestamp) {
						t.Errorf("query results not DESC-sorted by timestamp")
					}
				}
				_, _ = repo.GetRange(ctx, 0, 10)
				_ = repo.Len()
			}
			_ = r
		}()
	}

	wg.Wait()
	assert.Equal(t, writers*iterations, repo.Len())
	assert.Zero(t, readErrors.Load(), "concurrent reads should not error")
}
