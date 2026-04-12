package mem

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestFlagRepository_Create(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*FlagRepository)
		flag    *domain.FeatureFlag
		wantErr bool
		errCode errcode.Code
	}{
		{
			name: "success",
			flag: &domain.FeatureFlag{
				ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
			},
		},
		{
			name: "duplicate key returns error",
			setup: func(r *FlagRepository) {
				_ = r.Create(context.Background(), &domain.FeatureFlag{
					ID: "f1", Key: "dup-flag", Type: domain.FlagBoolean,
				})
			},
			flag:    &domain.FeatureFlag{ID: "f2", Key: "dup-flag", Type: domain.FlagBoolean},
			wantErr: true,
			errCode: errcode.ErrFlagDuplicate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewFlagRepository()
			if tc.setup != nil {
				tc.setup(repo)
			}

			err := repo.Create(context.Background(), tc.flag)
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

func TestFlagRepository_GetByKey(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
	}))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, "dark-mode")
		require.NoError(t, err)
		assert.Equal(t, "dark-mode", got.Key)
		assert.True(t, got.Enabled)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, "missing")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrFlagNotFound, ecErr.Code)
	})
}

func TestFlagRepository_Update(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: false,
	}))

	t.Run("success", func(t *testing.T) {
		require.NoError(t, repo.Update(ctx, &domain.FeatureFlag{
			ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
		}))
		got, err := repo.GetByKey(ctx, "dark-mode")
		require.NoError(t, err)
		assert.True(t, got.Enabled)
	})

	t.Run("not found", func(t *testing.T) {
		err := repo.Update(ctx, &domain.FeatureFlag{Key: "missing"})
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrFlagNotFound, ecErr.Code)
	})
}

func TestFlagRepository_List_SortByKey(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f1", Key: "z-flag", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f2", Key: "a-flag", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f3", Key: "m-flag", Type: domain.FlagBoolean,
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
	require.Len(t, result, 3)
	assert.Equal(t, "a-flag", result[0].Key)
	assert.Equal(t, "m-flag", result[1].Key)
	assert.Equal(t, "z-flag", result[2].Key)
}

func TestFlagRepository_List_SortByID(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f-z", Key: "flag-z", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f-a", Key: "flag-a", Type: domain.FlagBoolean,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "f-a", result[0].ID)
	assert.Equal(t, "f-z", result[1].ID)
}

func TestFlagRepository_List_UnknownField(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f1", Key: "flag-1", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f2", Key: "flag-2", Type: domain.FlagBoolean,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "unknown", Direction: query.SortASC}},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestFlagRepository_List_CursorPastEnd(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f1", Key: "flag-a", Type: domain.FlagBoolean,
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

func TestFlagRepository_List_WithCursor(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
			ID: "f-" + string(rune('a'+i)), Key: "flag-" + string(rune('a'+i)),
			Type: domain.FlagBoolean,
		}))
	}

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

	last := first[len(first)-1]
	params.CursorValues = []any{last.Key, last.ID}
	second, err := repo.List(ctx, params)
	require.NoError(t, err)
	for _, s := range second {
		for _, f := range first {
			assert.NotEqual(t, f.ID, s.ID, "cursor pagination should not repeat items")
		}
	}
}

func TestFlagRepository_List_DESC(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f1", Key: "flag-a", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f2", Key: "flag-z", Type: domain.FlagBoolean,
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
	assert.Equal(t, "flag-z", result[0].Key)
	assert.Equal(t, "flag-a", result[1].Key)
}

func TestFlagRepository_List_CursorDESC(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f-a", Key: "flag-a", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f-b", Key: "flag-b", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, &domain.FeatureFlag{
		ID: "f-c", Key: "flag-c", Type: domain.FlagBoolean,
	}))

	// DESC order: flag-c, flag-b, flag-a. Cursor after flag-b.
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"flag-b", "f-b"},
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, params)
	require.NoError(t, err)
	// After flag-b in DESC order: flag-a
	require.Len(t, result, 1)
	assert.Equal(t, "flag-a", result[0].Key)
}

func TestFlagRepository_List_Empty(t *testing.T) {
	repo := NewFlagRepository()
	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestFlagRepository_ConcurrentCRUDAndList verifies that concurrent
// CRUD and List calls do not race. Run with -race to verify.
func TestFlagRepository_ConcurrentCRUDAndList(t *testing.T) {
	repo := NewFlagRepository()
	ctx := context.Background()

	const writers = 5
	const readers = 10
	const iterations = 50

	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				_ = repo.Create(ctx, &domain.FeatureFlag{
					ID:  fmt.Sprintf("id-w%d-i%d", id, i),
					Key: fmt.Sprintf("flag-w%d-i%d", id, i),
				})
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
				_, _ = repo.List(ctx, params)
				_, _ = repo.GetByKey(ctx, "flag-w0-i0")
			}
			_ = r
		}()
	}

	wg.Wait()
}
