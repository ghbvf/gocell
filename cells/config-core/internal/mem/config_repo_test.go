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

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestConfigRepository_Create(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*ConfigRepository)
		entry   *domain.ConfigEntry
		wantErr bool
		errCode errcode.Code
	}{
		{
			name: "success",
			entry: &domain.ConfigEntry{
				ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
		},
		{
			name: "duplicate key returns error",
			setup: func(r *ConfigRepository) {
				_ = r.Create(context.Background(), &domain.ConfigEntry{
					ID: "cfg-1", Key: "dup-key", Value: "v1",
				})
			},
			entry:   &domain.ConfigEntry{ID: "cfg-2", Key: "dup-key", Value: "v2"},
			wantErr: true,
			errCode: errcode.ErrConfigDuplicate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewConfigRepository()
			if tc.setup != nil {
				tc.setup(repo)
			}

			err := repo.Create(context.Background(), tc.entry)
			if tc.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.ErrorAs(t, err, &ecErr)
				assert.Equal(t, tc.errCode, ecErr.Code)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigRepository_GetByKey(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, "app.name")
		require.NoError(t, err)
		assert.Equal(t, "gocell", got.Value)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, "missing")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrConfigNotFound, ecErr.Code)
	})
}

func TestConfigRepository_Update(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "old", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	t.Run("success", func(t *testing.T) {
		require.NoError(t, repo.Update(ctx, &domain.ConfigEntry{
			ID: "cfg-1", Key: "app.name", Value: "new", Version: 2,
			CreatedAt: now, UpdatedAt: time.Now(),
		}))
		got, err := repo.GetByKey(ctx, "app.name")
		require.NoError(t, err)
		assert.Equal(t, "new", got.Value)
	})

	t.Run("not found", func(t *testing.T) {
		err := repo.Update(ctx, &domain.ConfigEntry{Key: "missing"})
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrConfigNotFound, ecErr.Code)
	})
}

func TestConfigRepository_Delete(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "v",
	}))

	t.Run("success", func(t *testing.T) {
		require.NoError(t, repo.Delete(ctx, "app.name"))
		_, err := repo.GetByKey(ctx, "app.name")
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		err := repo.Delete(ctx, "missing")
		require.Error(t, err)
	})
}

func TestConfigRepository_List_SortByValue(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "banana", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "apple", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-3", Key: "k3", Value: "cherry", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "value", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, "apple", result[0].Value)
	assert.Equal(t, "banana", result[1].Value)
	assert.Equal(t, "cherry", result[2].Value)
}

func TestConfigRepository_List_SortByVersion(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 3,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-3", Key: "k3", Value: "v3", Version: 2,
		CreatedAt: now, UpdatedAt: now,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "version", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, 1, result[0].Version)
	assert.Equal(t, 2, result[1].Version)
	assert.Equal(t, 3, result[2].Version)
}

func TestConfigRepository_List_SortByKey(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "z-key", Value: "v1", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "a-key", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "a-key", result[0].Key)
	assert.Equal(t, "z-key", result[1].Key)
}

func TestConfigRepository_List_SortByCreatedAt(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: base, UpdatedAt: base,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "cfg-2", result[0].ID)
	assert.Equal(t, "cfg-1", result[1].ID)
}

func TestConfigRepository_List_SortByUpdatedAt(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: base, UpdatedAt: base.Add(2 * time.Hour),
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: base, UpdatedAt: base,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "updated_at", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "cfg-2", result[0].ID)
	assert.Equal(t, "cfg-1", result[1].ID)
}

func TestConfigRepository_List_UnknownField(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1",
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2",
	}))

	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "unknown", Direction: query.SortASC}},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestConfigRepository_List_CursorPastEnd(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: base, UpdatedAt: base,
	}))

	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"zzz-key", "zzz"},
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestConfigRepository_List_WithCursor(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
			ID: "cfg-" + string(rune('a'+i)), Key: "k" + string(rune('a'+i)),
			Value: "v", Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// First page
	params := query.ListParams{
		Limit: 2,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	first, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.True(t, len(first) > 0)

	// Second page using cursor from last item of first page
	last := first[len(first)-1]
	params.CursorValues = []any{last.Key, last.ID}
	second, err := repo.List(ctx, params)
	require.NoError(t, err)
	// No overlap between pages
	for _, s := range second {
		for _, f := range first {
			assert.NotEqual(t, f.ID, s.ID, "cursor pagination should not repeat items")
		}
	}
}

func TestConfigRepository_List_DESC(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k-a", Value: "v1", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k-z", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "k-z", result[0].Key)
	assert.Equal(t, "k-a", result[1].Key)
}

func TestConfigRepository_List_CursorDESC(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
			ID: "cfg-" + string(rune('a'+i)), Key: "k" + string(rune('a'+i)),
			Value: "v", Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	params := query.ListParams{
		Limit:        2,
		CursorValues: []any{"kb", "cfg-b"},
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	// After "kb" in DESC order: ka (which is "less than" kb)
	require.True(t, len(result) > 0)
	for _, r := range result {
		assert.True(t, r.Key < "kb" || (r.Key == "kb" && r.ID > "cfg-b"),
			"all results should be after cursor in DESC order")
	}
}

func TestConfigRepository_List_VersionCursor(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 3,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "cfg-3", Key: "k3", Value: "v3", Version: 5,
		CreatedAt: now, UpdatedAt: now,
	}))

	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{float64(3), "cfg-1"},
		Sort: []query.SortColumn{
			{Name: "version", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	// After version=3 in ASC order: version 5 (cfg-3)
	require.Len(t, result, 1)
	assert.Equal(t, "cfg-3", result[0].ID)
}

func TestConfigRepository_PublishVersion_And_GetVersion(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	require.NoError(t, repo.PublishVersion(ctx, &domain.ConfigVersion{
		ConfigID: "cfg-1", Version: 1, Value: "v1",
	}))
	require.NoError(t, repo.PublishVersion(ctx, &domain.ConfigVersion{
		ConfigID: "cfg-1", Version: 2, Value: "v2",
	}))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetVersion(ctx, "cfg-1", 2)
		require.NoError(t, err)
		assert.Equal(t, "v2", got.Value)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetVersion(ctx, "cfg-1", 99)
		require.Error(t, err)
	})
}

func TestConfigRepository_List_SubsecondPrecision_CreatedAt(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		now := base.Add(time.Duration(i*100) * time.Nanosecond)
		require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
			ID: fmt.Sprintf("id-%d", i), Key: fmt.Sprintf("key-%d", i),
			Value: "v", Version: 1, CreatedAt: now, UpdatedAt: now,
		}))
	}

	cursorTS := base.Add(100 * time.Nanosecond).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "id-1"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "id-2", result[0].ID)
}

func TestConfigRepository_List_SubsecondPrecision_UpdatedAt(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		now := base.Add(time.Duration(i*100) * time.Nanosecond)
		require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
			ID: fmt.Sprintf("id-%d", i), Key: fmt.Sprintf("key-%d", i),
			Value: "v", Version: 1, CreatedAt: base, UpdatedAt: now,
		}))
	}

	// DESC sort by updated_at, cursor at entry 1 (100ns).
	cursorTS := base.Add(100 * time.Nanosecond).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "id-1"},
		Sort: []query.SortColumn{
			{Name: "updated_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "id-0", result[0].ID)
}

// TestConfigRepository_ConcurrentCRUDAndList verifies that concurrent
// CRUD and List calls do not race and maintain semantic invariants.
func TestConfigRepository_ConcurrentCRUDAndList(t *testing.T) {
	repo := NewConfigRepository()
	ctx := context.Background()

	const writers = 5
	const readers = 10
	const iterations = 50

	var wg sync.WaitGroup
	var writeErrors, readErrors atomic.Int64

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				now := time.Now()
				if err := repo.Create(ctx, &domain.ConfigEntry{
					ID:        fmt.Sprintf("id-w%d-i%d", id, i),
					Key:       fmt.Sprintf("key-w%d-i%d", id, i),
					Value:     "val",
					Version:   1,
					CreatedAt: now,
					UpdatedAt: now,
				}); err != nil {
					writeErrors.Add(1)
				}
			}
		}(w)
	}

	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			params := query.ListParams{
				Limit: 10,
				Sort: []query.SortColumn{
					{Name: "key", Direction: query.SortASC},
					{Name: "id", Direction: query.SortASC},
				},
			}
			for range iterations {
				items, err := repo.List(ctx, params)
				if err != nil {
					readErrors.Add(1)
					continue
				}
				// Semantic invariant: results must be sorted.
				for j := 1; j < len(items); j++ {
					if items[j].Key < items[j-1].Key {
						t.Errorf("list results not sorted: %s < %s", items[j].Key, items[j-1].Key)
					}
				}
			}
			_ = r
		}()
	}

	wg.Wait()
	assert.Zero(t, writeErrors.Load(), "concurrent writes should not error (unique keys)")
	assert.Zero(t, readErrors.Load(), "concurrent reads should not error")
}
