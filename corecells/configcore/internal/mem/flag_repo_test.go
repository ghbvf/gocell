package mem

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// testFlagTenant reuses the package-level testTenant / testTenantB defined in
// config_repo_test.go. Both test files are in the same package (mem), so the
// vars are shared.

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
				_ = r.Create(context.Background(), testTenant, &domain.FeatureFlag{
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
			repo := NewFlagRepository(clock.Real())
			if tc.setup != nil {
				tc.setup(repo)
			}

			err := repo.Create(context.Background(), testTenant, tc.flag)
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
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
	}))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, testTenant, "dark-mode")
		require.NoError(t, err)
		assert.Equal(t, "dark-mode", got.Key)
		assert.True(t, got.Enabled)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, testTenant, "missing")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrFlagNotFound, ecErr.Code)
	})
}

func TestFlagRepository_Update(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: false, Version: 1,
	}))

	t.Run("success", func(t *testing.T) {
		got, err := repo.Update(ctx, testTenant, "dark-mode", 1, true, 0, "")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, got.Enabled)
		assert.Equal(t, 2, got.Version, "version must be incremented")
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.Update(ctx, testTenant, "missing", 1, false, 0, "")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrFlagNotFound, ecErr.Code)
	})

	t.Run("version mismatch returns ErrVersionConflict", func(t *testing.T) {
		_, err := repo.Update(ctx, testTenant, "dark-mode", 999, false, 0, "")
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrVersionConflict, ecErr.Code)
	})
}

func TestFlagRepository_List_SortByKey(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f1", Key: "z-flag", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f2", Key: "a-flag", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f3", Key: "m-flag", Type: domain.FlagBoolean,
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
	require.Len(t, result, 3)
	assert.Equal(t, "a-flag", result[0].Key)
	assert.Equal(t, "m-flag", result[1].Key)
	assert.Equal(t, "z-flag", result[2].Key)
}

func TestFlagRepository_List_SortByID(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f-z", Key: "flag-z", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f-a", Key: "flag-a", Type: domain.FlagBoolean,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "f-a", result[0].ID)
	assert.Equal(t, "f-z", result[1].ID)
}

func TestFlagRepository_List_UnknownField(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f1", Key: "flag-1", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f2", Key: "flag-2", Type: domain.FlagBoolean,
	}))

	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "unknown", Direction: query.SortASC}},
	}
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestFlagRepository_List_CursorPastEnd(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestFlagRepository_List_WithCursor(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	for i := range 5 {
		require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
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
	first, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	require.True(t, len(first) > 0)

	last := first[len(first)-1]
	params.CursorValues = []any{last.Key, last.ID}
	second, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	for _, s := range second {
		for _, f := range first {
			assert.NotEqual(t, f.ID, s.ID, "cursor pagination should not repeat items")
		}
	}
}

func TestFlagRepository_List_DESC(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f1", Key: "flag-a", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f2", Key: "flag-z", Type: domain.FlagBoolean,
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
	assert.Equal(t, "flag-z", result[0].Key)
	assert.Equal(t, "flag-a", result[1].Key)
}

func TestFlagRepository_List_CursorDESC(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f-a", Key: "flag-a", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f-b", Key: "flag-b", Type: domain.FlagBoolean,
	}))
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
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
	result, err := repo.List(ctx, testTenant, params)
	require.NoError(t, err)
	// After flag-b in DESC order: flag-a
	require.Len(t, result, 1)
	assert.Equal(t, "flag-a", result[0].Key)
}

func TestFlagRepository_List_Empty(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	result, err := repo.List(context.Background(), testTenant, params)
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestFlagRepository_ConcurrentCRUDAndList verifies that concurrent
// CRUD and List calls do not race and maintain semantic invariants.
// flagConcurrentWriterN creates and updates `iterations` feature flags for writer id,
// incrementing writeErrors on any failure. Extracted from
// TestFlagRepository_ConcurrentCRUDAndList to reduce cognitive complexity.
func flagConcurrentWriterN(ctx context.Context, repo *FlagRepository, id, iterations int, writeErrors *atomic.Int64) {
	for i := range iterations {
		key := fmt.Sprintf("flag-w%d-i%d", id, i)
		if err := repo.Create(ctx, testTenant, &domain.FeatureFlag{
			ID:   fmt.Sprintf("id-w%d-i%d", id, i),
			Key:  key,
			Type: domain.FlagBoolean,
		}); err != nil {
			writeErrors.Add(1)
			continue
		}
		// Update the flag we just created (version starts at 0 for Create).
		if _, err := repo.Update(ctx, testTenant, key, 0, true, 0, ""); err != nil {
			writeErrors.Add(1)
		}
	}
}

// flagConcurrentReaderN runs `iterations` sorted-list reads against the repo,
// counting errors and asserting sort order invariant. Extracted from
// TestFlagRepository_ConcurrentCRUDAndList to reduce cognitive complexity.
func flagConcurrentReaderN(t *testing.T, ctx context.Context, repo *FlagRepository, iterations int, readErrors *atomic.Int64) {
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
				t.Errorf("flag list not sorted: %s < %s", items[j].Key, items[j-1].Key)
			}
		}
	}
}

func TestFlagRepository_ConcurrentCRUDAndList(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
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
			flagConcurrentWriterN(ctx, repo, id, iterations, &writeErrors)
		}(w)
	}

	for range readers {
		wg.Go(func() {
			flagConcurrentReaderN(t, ctx, repo, iterations, &readErrors)
		})
	}

	wg.Wait()
	assert.Zero(t, writeErrors.Load(), "concurrent writes should not error (unique keys)")
	assert.Zero(t, readErrors.Load(), "concurrent reads should not error")
}

// TestFlagRepository_CrossTenantIsolation verifies that a flag written under
// testTenant is invisible to testTenantB on every read/write path:
// GetByKey/Update/Toggle/Delete return ErrFlagNotFound and List returns empty
// under the wrong tenant; same-tenant access still succeeds.
// Mirrors the config-repo cross-tenant isolation test.
func TestFlagRepository_CrossTenantIsolation(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
	ctx := context.Background()

	const key = "cross.tenant.flag"
	require.NoError(t, repo.Create(ctx, testTenant, &domain.FeatureFlag{
		ID: "f-cross-1", Key: key, Type: domain.FlagBoolean, Enabled: true, Version: 1,
	}))

	assertNotFound := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, errcode.ErrFlagNotFound, ecErr.Code,
			"cross-tenant access must return ErrFlagNotFound, not leak the flag")
	}

	t.Run("GetByKey_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, testTenantB, key)
		assertNotFound(t, err)
	})
	t.Run("GetByKey_under_tenantA_succeeds", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, testTenant, key)
		require.NoError(t, err)
		assert.True(t, got.Enabled)
	})
	t.Run("Update_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.Update(ctx, testTenantB, key, 1, false, 0, "")
		assertNotFound(t, err)
	})
	t.Run("Toggle_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.Toggle(ctx, testTenantB, key, 1, false)
		assertNotFound(t, err)
	})
	t.Run("Delete_under_tenantB_not_found", func(t *testing.T) {
		_, err := repo.Delete(ctx, testTenantB, key, 1)
		assertNotFound(t, err)
	})
	t.Run("List_under_tenantB_empty", func(t *testing.T) {
		flags, err := repo.List(ctx, testTenantB, query.ListParams{
			Limit: 50,
			Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}},
		})
		require.NoError(t, err)
		for _, f := range flags {
			assert.NotEqual(t, key, f.Key, "tenant B must not see tenant A's flag in List")
		}
	})
}

// TestFlagRepository_EmptyTenantGuard verifies that every data method rejects a
// zero/empty tenant.TenantID with ErrValidationFailed (the t.Validate() guard).
// Mirrors the config-repo empty-tenant guard test.
func TestFlagRepository_EmptyTenantGuard(t *testing.T) {
	repo := NewFlagRepository(clock.Real())
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
		{"Create", func() error {
			return repo.Create(ctx, zero, &domain.FeatureFlag{ID: "x", Key: "k", Type: domain.FlagBoolean})
		}},
		{"GetByKey", func() error { _, err := repo.GetByKey(ctx, zero, "k"); return err }},
		{"Update", func() error { _, err := repo.Update(ctx, zero, "k", 1, true, 0, ""); return err }},
		{"Toggle", func() error { _, err := repo.Toggle(ctx, zero, "k", 1, true); return err }},
		{"Delete", func() error { _, err := repo.Delete(ctx, zero, "k", 1); return err }},
		{"List", func() error { _, err := repo.List(ctx, zero, query.ListParams{Limit: 10}); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertValidationErr(t, tc.call())
		})
	}
}
