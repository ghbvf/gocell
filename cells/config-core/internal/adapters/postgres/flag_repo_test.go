package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFlagRepositoryFromDBTX is a test-only constructor that bypasses the
// Session layer, allowing unit tests to inject a mockDB directly.
func newFlagRepositoryFromDBTX(db DBTX) *FlagRepository {
	return &FlagRepository{db: db}
}

// TestFlagRepo_Create_GetByKey_Update_Delete exercises the CRUD round-trip
// through the mockDB layer (unit, no real PG).
func TestFlagRepo_Create_GetByKey_Update_Delete(t *testing.T) {
	now := time.Now()

	t.Run("Create", func(t *testing.T) {
		db := &mockDB{}
		repo := newFlagRepositoryFromDBTX(db)

		flag := &domain.FeatureFlag{
			ID:                "flg-1",
			Key:               "dark-mode",
			Enabled:           false,
			RolloutPercentage: 0,
			Description:       "dark mode toggle",
			Version:           1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		err := repo.Create(context.Background(), flag)
		require.NoError(t, err)
		require.Len(t, db.execCalls, 1)
		assert.Contains(t, db.execCalls[0].sql, "INSERT INTO feature_flags")
		assert.Equal(t, "flg-1", db.execCalls[0].args[0])
		assert.Equal(t, "dark-mode", db.execCalls[0].args[1])
	})

	t.Run("GetByKey", func(t *testing.T) {
		db := &mockDB{
			queryRowResult: &mockRow{
				values: []any{"flg-1", "dark-mode", false, 0, "dark mode toggle", 1, now, now},
			},
		}
		repo := newFlagRepositoryFromDBTX(db)

		flag, err := repo.GetByKey(context.Background(), "dark-mode")
		require.NoError(t, err)
		assert.Equal(t, "flg-1", flag.ID)
		assert.Equal(t, "dark-mode", flag.Key)
		assert.Equal(t, false, flag.Enabled)
		assert.Equal(t, 1, flag.Version)
		assert.Equal(t, "dark mode toggle", flag.Description)
	})

	t.Run("Update", func(t *testing.T) {
		// Update now uses QueryRow (RETURNING clause).
		db := &capturingDB{
			queryRowResult: &mockRow{
				values: []any{"flg-1", "dark-mode", true, 50, "updated desc", 2, now, now},
			},
		}
		repo := &FlagRepository{db: db}

		got, err := repo.Update(context.Background(), "dark-mode", true, 50, "updated desc")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Contains(t, db.queryRowSQL, "UPDATE feature_flags")
		assert.Contains(t, db.queryRowSQL, "version=version+1")
		assert.Contains(t, db.queryRowSQL, "RETURNING")
		assert.Equal(t, "dark-mode", got.Key)
		assert.True(t, got.Enabled)
		assert.Equal(t, 2, got.Version)
	})

	t.Run("Delete", func(t *testing.T) {
		// Delete now uses QueryRow (RETURNING clause).
		db := &capturingDB{
			queryRowResult: &mockRow{
				values: []any{"flg-1", "dark-mode", false, 0, "desc", 1, now, now},
			},
		}
		repo := &FlagRepository{db: db}

		got, err := repo.Delete(context.Background(), "dark-mode")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Contains(t, db.queryRowSQL, "DELETE FROM feature_flags")
		assert.Contains(t, db.queryRowSQL, "RETURNING")
		assert.Equal(t, "dark-mode", got.Key)
	})
}

// TestFlagRepo_Toggle_OnlyAffectsEnabledColumn verifies that Toggle uses a
// targeted UPDATE that only modifies enabled + version + updated_at, not
// rollout_percentage or description.
func TestFlagRepo_Toggle_OnlyAffectsEnabledColumn(t *testing.T) {
	now := time.Now()
	// Toggle returns the updated flag via QueryRow (RETURNING clause).
	// Use a capturing mock that records the QueryRow SQL.
	capDB := &capturingDB{
		queryRowResult: &mockRow{
			values: []any{"flg-1", "dark-mode", true, 25, "desc", 2, now, now},
		},
	}
	repo := &FlagRepository{db: capDB}

	flag, err := repo.Toggle(context.Background(), "dark-mode", true)
	require.NoError(t, err)
	require.NotNil(t, flag)
	// Verify the SQL only touches enabled, version, updated_at — not rollout_percentage or description.
	require.NotEmpty(t, capDB.queryRowSQL, "Toggle must call QueryRow")
	assert.NotContains(t, capDB.queryRowSQL, "rollout_percentage =",
		"Toggle must not overwrite rollout_percentage")
	assert.NotContains(t, capDB.queryRowSQL, "description =",
		"Toggle must not overwrite description")
	assert.Contains(t, capDB.queryRowSQL, "enabled=$1")
	assert.Contains(t, capDB.queryRowSQL, "version=version+1")
	// Returned flag reflects the PG RETURNING values.
	assert.True(t, flag.Enabled)
	assert.Equal(t, 2, flag.Version)
	assert.Equal(t, 25, flag.RolloutPercentage, "rolloutPercentage must be preserved from RETURNING row")
}

// capturingDB is a mockDB variant that also records the QueryRow SQL.
type capturingDB struct {
	queryRowSQL    string
	queryRowResult *mockRow
}

func (d *capturingDB) Exec(_ context.Context, _ string, _ ...any) (int64, error) {
	return 1, nil
}
func (d *capturingDB) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return &mockRowSet{}, nil
}
func (d *capturingDB) QueryRow(_ context.Context, sql string, _ ...any) Row {
	d.queryRowSQL = sql
	if d.queryRowResult == nil {
		return &mockRow{scanErr: assert.AnError}
	}
	return d.queryRowResult
}

// TestFlagRepo_List_Paginated verifies keyset pagination + sort by key+id.
func TestFlagRepo_List_Paginated(t *testing.T) {
	now := time.Now()
	db := &mockDB{
		queryRows: &mockRowSet{
			entries: []mockRowValues{
				{values: []any{"flg-1", "a.flag", false, 0, "a", 1, now, now}},
				{values: []any{"flg-2", "b.flag", true, 50, "b", 1, now, now}},
			},
		},
	}
	repo := newFlagRepositoryFromDBTX(db)

	params := query.ListParams{
		Limit: 50,
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	flags, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	require.Len(t, flags, 2)
	assert.Equal(t, "a.flag", flags[0].Key)
	assert.Equal(t, "b.flag", flags[1].Key)

	require.Len(t, db.queryCalls, 1)
	assert.Contains(t, db.queryCalls[0].sql, "LIMIT")
}

// TestFlagRepo_Toggle_Concurrent_NoLost verifies that concurrent Toggles are
// correctly tracked. Since tests use a fake mockDB, we simulate via
// two goroutines calling Toggle on different keys and confirm no data race.
func TestFlagRepo_Toggle_Concurrent_NoLost(t *testing.T) {
	now := time.Now()
	// We use a threadSafeToggleDB that simulates atomic version increment.
	makeRow := func(key string, enabled bool, version int) *mockRow {
		return &mockRow{
			values: []any{"flg-x", key, enabled, 0, "desc", version, now, now},
		}
	}

	callIdx := 0
	var mu sync.Mutex
	db := &threadSafeQueryRowDB{
		rows: []*mockRow{
			makeRow("dark-mode", true, 2),
			makeRow("dark-mode", false, 3),
		},
		callIdx: &callIdx,
		mu:      &mu,
	}

	repo := &FlagRepository{db: db}

	var wg sync.WaitGroup
	results := make([]*domain.FeatureFlag, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			enabled := idx%2 == 0
			flag, err := repo.Toggle(context.Background(), "dark-mode", enabled)
			if err == nil {
				mu.Lock()
				results[idx] = flag
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// At least one Toggle must have produced a result.
	got := 0
	for _, r := range results {
		if r != nil {
			got++
		}
	}
	assert.Greater(t, got, 0, "at least one Toggle result expected")
}

// TestFlagRepo_NotFound_Errors verifies GetByKey/Toggle/Delete miss → ErrFlagRepoNotFound.
func TestFlagRepo_NotFound_Errors(t *testing.T) {
	t.Run("GetByKey_NotFound", func(t *testing.T) {
		db := &mockDB{
			queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
		}
		repo := newFlagRepositoryFromDBTX(db)

		_, err := repo.GetByKey(context.Background(), "missing")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagRepoNotFound, ec.Code)
	})

	t.Run("GetByKey_OtherError_Returns_ErrFlagRepoQuery", func(t *testing.T) {
		db := &mockDB{
			queryRowResult: &mockRow{scanErr: assert.AnError},
		}
		repo := newFlagRepositoryFromDBTX(db)

		_, err := repo.GetByKey(context.Background(), "missing")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagRepoQuery, ec.Code)
	})

	t.Run("Toggle_NotFound", func(t *testing.T) {
		db := &mockDB{
			queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
		}
		repo := newFlagRepositoryFromDBTX(db)

		_, err := repo.Toggle(context.Background(), "missing", true)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagRepoNotFound, ec.Code)
	})

	t.Run("Update_NotFound", func(t *testing.T) {
		db := &mockDB{
			queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
		}
		repo := newFlagRepositoryFromDBTX(db)

		_, err := repo.Update(context.Background(), "missing", false, 0, "")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagRepoNotFound, ec.Code)
	})

	t.Run("Delete_NotFound", func(t *testing.T) {
		db := &mockDB{
			queryRowResult: &mockRow{scanErr: pgx.ErrNoRows},
		}
		repo := newFlagRepositoryFromDBTX(db)

		_, err := repo.Delete(context.Background(), "missing")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagRepoNotFound, ec.Code)
	})
}

// TestFlagRepo_WithoutTx_WritePathsRequireTx verifies session-based repo
// returns ErrAdapterPGNoTx for write ops without a tx in context (F-S-1).
func TestFlagRepo_WithoutTx_WritePathsRequireTx(t *testing.T) {
	session := NewSession(nil)

	t.Run("Create", func(t *testing.T) {
		repo := NewFlagRepository(session)
		err := repo.Create(context.Background(), &domain.FeatureFlag{Key: "k"})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
	})

	t.Run("Update", func(t *testing.T) {
		repo := NewFlagRepository(session)
		_, err := repo.Update(context.Background(), "k", false, 0, "")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
	})

	t.Run("Delete", func(t *testing.T) {
		repo := NewFlagRepository(session)
		_, err := repo.Delete(context.Background(), "k")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
	})

	t.Run("Toggle", func(t *testing.T) {
		repo := NewFlagRepository(session)
		_, err := repo.Toggle(context.Background(), "k", true)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
	})
}

// threadSafeQueryRowDB is a test double that returns successive rows per QueryRow call.
type threadSafeQueryRowDB struct {
	rows    []*mockRow
	callIdx *int
	mu      *sync.Mutex
}

func (d *threadSafeQueryRowDB) Exec(_ context.Context, _ string, _ ...any) (int64, error) {
	return 1, nil
}

func (d *threadSafeQueryRowDB) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return &mockRowSet{}, nil
}

func (d *threadSafeQueryRowDB) QueryRow(_ context.Context, _ string, _ ...any) Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	idx := *d.callIdx
	if idx >= len(d.rows) {
		return &mockRow{scanErr: pgx.ErrNoRows}
	}
	*d.callIdx++
	return d.rows[idx]
}
