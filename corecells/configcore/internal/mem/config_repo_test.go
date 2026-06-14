package mem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

const (
	// configD2h is used for seeding time-sorted config entries 2 hours apart.
	configD2h = 2 * time.Hour
	// configNs100 is used in sub-second cursor precision tests.
	configNs100 = 100 * time.Nanosecond
)

// testTenant is a canonical test tenant UUID used across all config-repo tests.
var testTenant = tenant.TenantID("00000000-0000-0000-0000-000000000001")

// testTenantB is a second canonical test tenant UUID used by cross-tenant
// isolation tests (mirrors the PG integration testTenantB).
var testTenantB = tenant.TenantID("00000000-0000-0000-0000-000000000002")

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
				_ = r.Create(context.Background(), testTenant, &domain.ConfigEntry{
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
			repo := NewConfigRepository(clock.Real())
			if tc.setup != nil {
				tc.setup(repo)
			}

			err := repo.Create(context.Background(), testTenant, tc.entry)
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
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, testTenant, "app.name")
		require.NoError(t, err)
		assert.Equal(t, "gocell", got.Value)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, testTenant, "missing")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ecErr.Code)
	})
}

func TestConfigRepository_Update(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "old", Sensitive: false, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	t.Run("success preserves sensitive flag", func(t *testing.T) {
		updated, err := repo.Update(ctx, testTenant, "app.name", 1, "new")
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, "new", updated.Value)
		assert.Equal(t, 2, updated.Version)
		assert.False(t, updated.Sensitive, "Update must preserve the existing sensitive flag")
		got, err := repo.GetByKey(ctx, testTenant, "app.name")
		require.NoError(t, err)
		assert.Equal(t, "new", got.Value)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.Update(ctx, testTenant, "missing", 1, "v")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ecErr.Code)
	})

	t.Run("version mismatch returns ErrVersionConflict", func(t *testing.T) {
		_, err := repo.Update(ctx, testTenant, "app.name", 999, "v")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrVersionConflict, ecErr.Code)
	})
}

func TestConfigRepository_UpdateForRollback(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "old", Sensitive: false, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	t.Run("changes sensitive flag", func(t *testing.T) {
		updated, err := repo.UpdateForRollback(ctx, testTenant, "app.name", 1, "secret", true)
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, "secret", updated.Value)
		assert.True(t, updated.Sensitive, "UpdateForRollback must set the provided sensitive flag")
		assert.Equal(t, 2, updated.Version)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.UpdateForRollback(ctx, testTenant, "missing", 1, "v", false)
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ecErr.Code)
	})

	t.Run("version mismatch returns ErrVersionConflict", func(t *testing.T) {
		_, err := repo.UpdateForRollback(ctx, testTenant, "app.name", 999, "v", false)
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrVersionConflict, ecErr.Code)
	})
}

func TestConfigRepository_Delete(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		repo := NewConfigRepository(clock.Real())
		require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
			ID: "cfg-1", Key: "app.name", Value: "v", Version: 1,
		}))
		deleted, err := repo.Delete(ctx, testTenant, "app.name", 1)
		require.NoError(t, err)
		require.NotNil(t, deleted)
		assert.Equal(t, "app.name", deleted.Key)
		assert.Equal(t, "v", deleted.Value)
		_, err = repo.GetByKey(ctx, testTenant, "app.name")
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		repo := NewConfigRepository(clock.Real())
		_, err := repo.Delete(ctx, testTenant, "missing", 1)
		require.Error(t, err)
	})

	t.Run("version mismatch returns ErrVersionConflict", func(t *testing.T) {
		repo := NewConfigRepository(clock.Real())
		require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
			ID: "cfg-1", Key: "app.name", Value: "v", Version: 1,
		}))
		_, err := repo.Delete(ctx, testTenant, "app.name", 999)
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrVersionConflict, ecErr.Code)
	})
}

func TestConfigRepository_List_SortByValue(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "banana", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "apple", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, "apple", result[0].Value)
	assert.Equal(t, "banana", result[1].Value)
	assert.Equal(t, "cherry", result[2].Value)
}

func TestConfigRepository_List_SortByVersion(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 3,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, 1, result[0].Version)
	assert.Equal(t, 2, result[1].Version)
	assert.Equal(t, 3, result[2].Version)
}

func TestConfigRepository_List_SortByKey(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "z-key", Value: "v1", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "a-key", result[0].Key)
	assert.Equal(t, "z-key", result[1].Key)
}

func TestConfigRepository_List_SortByCreatedAt(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: base.Add(configD2h), UpdatedAt: base,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "cfg-2", result[0].ID)
	assert.Equal(t, "cfg-1", result[1].ID)
}

func TestConfigRepository_List_SortByUpdatedAt(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: base, UpdatedAt: base.Add(configD2h),
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "cfg-2", result[0].ID)
	assert.Equal(t, "cfg-1", result[1].ID)
}

func TestConfigRepository_List_UnknownField(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1",
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2",
	}))

	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "unknown", Direction: query.SortASC}},
	}
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestConfigRepository_List_CursorPastEnd(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestConfigRepository_List_WithCursor(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	for i := range 5 {
		require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	first, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.True(t, len(first) > 0)

	// Second page using cursor from last item of first page
	last := first[len(first)-1]
	params.CursorValues = []any{last.Key, last.ID}
	second, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	// No overlap between pages
	for _, s := range second {
		for _, f := range first {
			assert.NotEqual(t, f.ID, s.ID, "cursor pagination should not repeat items")
		}
	}
}

func TestConfigRepository_List_DESC(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k-a", Value: "v1", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "k-z", result[0].Key)
	assert.Equal(t, "k-a", result[1].Key)
}

func TestConfigRepository_List_CursorDESC(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	for i := range 5 {
		require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	// After "kb" in DESC order: ka (which is "less than" kb)
	require.True(t, len(result) > 0)
	for _, r := range result {
		assert.True(t, r.Key < "kb" || (r.Key == "kb" && r.ID > "cfg-b"),
			"all results should be after cursor in DESC order")
	}
}

func TestConfigRepository_List_VersionCursor(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 3,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	// After version=3 in ASC order: version 5 (cfg-3)
	require.Len(t, result, 1)
	assert.Equal(t, "cfg-3", result[0].ID)
}

func TestConfigRepository_PublishVersion_And_GetVersion(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.PublishVersion(ctx, testTenant, &domain.ConfigVersion{
		ConfigID: "cfg-1", Version: 1, Value: "v1",
	}))
	require.NoError(t, repo.PublishVersion(ctx, testTenant, &domain.ConfigVersion{
		ConfigID: "cfg-1", Version: 2, Value: "v2",
	}))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetVersion(ctx, testTenant, "cfg-1", 2)
		require.NoError(t, err)
		assert.Equal(t, "v2", got.Value)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetVersion(ctx, testTenant, "cfg-1", 99)
		require.Error(t, err)
	})
}

func TestConfigRepository_List_SubsecondPrecision_CreatedAt(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		now := base.Add(time.Duration(i*100) * time.Nanosecond)
		require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
			ID: fmt.Sprintf("id-%d", i), Key: fmt.Sprintf("key-%d", i),
			Value: "v", Version: 1, CreatedAt: now, UpdatedAt: now,
		}))
	}

	cursorTS := base.Add(configNs100).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "id-1"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "id-2", result[0].ID)
}

func TestConfigRepository_List_SubsecondPrecision_UpdatedAt(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		now := base.Add(time.Duration(i*100) * time.Nanosecond)
		require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
			ID: fmt.Sprintf("id-%d", i), Key: fmt.Sprintf("key-%d", i),
			Value: "v", Version: 1, CreatedAt: base, UpdatedAt: now,
		}))
	}

	// DESC sort by updated_at, cursor at entry 1 (100ns).
	cursorTS := base.Add(configNs100).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{cursorTS, "id-1"},
		Sort: []query.SortColumn{
			{Name: "updated_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "id-0", result[0].ID)
}

// TestConfigRepository_ConcurrentCRUDAndList verifies that concurrent
// CRUD and List calls do not race and maintain semantic invariants.
// configConcurrentWriterN creates `iterations` unique ConfigEntry rows for writer id,
// incrementing writeErrors on any failure. Extracted from
// TestConfigRepository_ConcurrentCRUDAndList to reduce cognitive complexity.
func configConcurrentWriterN(ctx context.Context, repo *ConfigRepository, id, iterations int, writeErrors *atomic.Int64) {
	for i := range iterations {
		now := time.Now()
		if err := repo.Create(ctx, testTenant, &domain.ConfigEntry{
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
}

// configConcurrentReaderN runs `iterations` sorted-list reads against the repo,
// counting errors and asserting sort order invariant. Extracted from
// TestConfigRepository_ConcurrentCRUDAndList to reduce cognitive complexity.
func configConcurrentReaderN(t *testing.T, ctx context.Context, repo *ConfigRepository, iterations int, readErrors *atomic.Int64) {
	t.Helper()
	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	for range iterations {
		items, err := repo.List(ctx, testTenant, params)
		if err != nil {
			readErrors.Add(1)
			continue
		}
		for j := 1; j < len(items); j++ {
			if items[j].Key < items[j-1].Key {
				t.Errorf("list results not sorted: %s < %s", items[j].Key, items[j-1].Key)
			}
		}
	}
}

func TestConfigRepository_ConcurrentCRUDAndList(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
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
			configConcurrentWriterN(ctx, repo, id, iterations, &writeErrors)
		}(w)
	}

	for range readers {
		wg.Go(func() {
			configConcurrentReaderN(t, ctx, repo, iterations, &readErrors)
		})
	}

	wg.Wait()
	assert.Zero(t, writeErrors.Load(), "concurrent writes should not error (unique keys)")
	assert.Zero(t, readErrors.Load(), "concurrent reads should not error")
}

// casUpdateRetryWorker performs a read-then-CAS Update retry loop against the
// shared repo for a single concurrent writer. Extracted from
// TestConcurrentUpdate_CAS so each branch (success / unexpected error /
// retry-on-conflict) stays linear, reducing the parent test's cognitive
// complexity below the lint cap.
func casUpdateRetryWorker(ctx context.Context, repo *ConfigRepository, key, value string) {
	for {
		cur, err := repo.GetByKey(ctx, testTenant, key)
		if err != nil {
			return
		}
		if _, updateErr := repo.Update(ctx, testTenant, key, cur.Version, value); updateErr == nil {
			return
		} else if !isVersionConflict(updateErr) {
			return // unexpected error — abandon this worker
		}
		// ErrVersionConflict: yield and retry until our CAS wins.
	}
}

// isVersionConflict reports whether err is the standard CAS conflict signal.
// Centralized so concurrent-test loops do not duplicate the errors.As pattern.
func isVersionConflict(err error) bool {
	var ecErr *errcode.Error
	return errors.As(err, &ecErr) && ecErr.Code == errcode.ErrVersionConflict
}

// TestConfigRepository_RepoReady verifies that the in-memory ConfigRepository
// always returns nil from RepoReady (MemStore convention — no external dependency).
func TestConfigRepository_RepoReady(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	assert.NoError(t, repo.RepoReady(context.Background()), "in-memory RepoReady must always return nil")
}

// TestConfigRepository_RepoReady_Conformance wires the mem repo through the
// single-source celltest harness. broken=nil signals "no differentiated failure
// domain" — the harness skips the broken sub-test with a clear message.
func TestConfigRepository_RepoReady_Conformance(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	celltest.RunRepoReadinessConformance(t, "configcore-mem", repo, nil)
}

// TestConcurrentUpdate_CAS verifies that concurrent Update calls on the same key
// with read-then-CAS retry each succeed exactly once: each goroutine retries on
// ErrVersionConflict until it wins, resulting in exactly N increments total.
func TestConcurrentUpdate_CAS(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	now := time.Now()
	_ = repo.Create(context.Background(), testTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k", Value: "v0", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})

	const updates = 10
	var wg sync.WaitGroup
	wg.Add(updates)
	for i := range updates {
		go func(i int) {
			defer wg.Done()
			casUpdateRetryWorker(context.Background(), repo, "k", fmt.Sprintf("v%d", i))
		}(i)
	}
	wg.Wait()

	entry, err := repo.GetByKey(context.Background(), testTenant, "k")
	require.NoError(t, err)
	assert.Equal(t, updates+1, entry.Version, "each CAS Update retry must eventually succeed; version must reach initial+N")
}

// TestConfigRepository_CrossTenantIsolation verifies that an entry written under
// testTenant is invisible to testTenantB on every read/write path:
// GetByKey/Update/Delete/GetVersion return ErrConfigRepoNotFound and List
// returns empty under the wrong tenant; same-tenant access still succeeds.
// Mirrors the PG TestConfigRepo_Integration_CrossTenantIsolation.
func TestConfigRepository_CrossTenantIsolation(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()

	now := time.Now()
	const key = "cross.tenant.key"
	const entryID = "cfg-cross-1"
	require.NoError(t, repo.Create(ctx, testTenant, &domain.ConfigEntry{
		ID: entryID, Key: key, Value: "tenant-a-only", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.PublishVersion(ctx, testTenant, &domain.ConfigVersion{
		ID: "cv-cross-1", ConfigID: entryID, Version: 1, Value: "tenant-a-only",
	}))

	assertNotFound := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ecErr.Code,
			"cross-tenant access must return ErrConfigRepoNotFound, not leak the entry")
	}

	t.Run("GetByKey_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, testTenantB, key)
		assertNotFound(t, err)
	})
	t.Run("GetByKey_under_tenantA_succeeds", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, testTenant, key)
		require.NoError(t, err)
		assert.Equal(t, "tenant-a-only", got.Value)
	})
	t.Run("Update_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.Update(ctx, testTenantB, key, 1, "should-not-apply")
		assertNotFound(t, err)
	})
	t.Run("UpdateForRollback_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.UpdateForRollback(ctx, testTenantB, key, 1, "should-not-apply", true)
		assertNotFound(t, err)
	})
	t.Run("Delete_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.Delete(ctx, testTenantB, key, 1)
		assertNotFound(t, err)
	})
	t.Run("List_under_tenantB_empty", func(t *testing.T) {
		entries, err := repo.List(ctx, testTenantB, query.ListParams{
			Limit: 50,
			Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}},
		})
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotEqual(t, key, e.Key, "tenant B must not see tenant A's entry in List")
		}
	})
	t.Run("GetVersion_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.GetVersion(ctx, testTenantB, entryID, 1)
		assertNotFound(t, err)
	})
	t.Run("GetVersion_under_tenantA_succeeds", func(t *testing.T) {
		_, err := repo.GetVersion(ctx, testTenant, entryID, 1)
		require.NoError(t, err)
	})
}

// TestConfigRepository_EmptyTenantGuard verifies that every data method rejects
// a zero/empty tenant.TenantID with ErrValidationFailed (the t.Validate() guard)
// rather than silently operating on the empty-tenant inner map. RepoReady is
// excluded — it is a schema-existence probe, not a tenant-scoped data read.
// Mirrors the PG TestConfigRepo_Integration_EmptyTenantGuard.
func TestConfigRepository_EmptyTenantGuard(t *testing.T) {
	repo := NewConfigRepository(clock.Real())
	ctx := context.Background()
	zero := tenant.TenantID("") // intentionally invalid

	assertValidationErr := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "empty tenant must return error")
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code,
			"empty tenant must return ErrValidationFailed, not a not-found/duplicate error")
	}

	tests := []struct {
		name string
		call func() error
	}{
		{"Create", func() error { return repo.Create(ctx, zero, &domain.ConfigEntry{ID: "x", Key: "k"}) }},
		{"GetByKey", func() error { _, err := repo.GetByKey(ctx, zero, "k"); return err }},
		{"Update", func() error { _, err := repo.Update(ctx, zero, "k", 1, "v"); return err }},
		{"UpdateForRollback", func() error { _, err := repo.UpdateForRollback(ctx, zero, "k", 1, "v", false); return err }},
		{"Delete", func() error { _, err := repo.Delete(ctx, zero, "k", 1); return err }},
		{"List", func() error { _, err := repo.List(ctx, zero, query.ListParams{Limit: 10}); return err }},
		{"PublishVersion", func() error { return repo.PublishVersion(ctx, zero, &domain.ConfigVersion{ConfigID: "x", Version: 1}) }},
		{"GetVersion", func() error { _, err := repo.GetVersion(ctx, zero, "x", 1); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertValidationErr(t, tc.call())
		})
	}
}
