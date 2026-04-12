package mem

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestOrderRepository_Create(t *testing.T) {
	tests := []struct {
		name    string
		order   *domain.Order
		setup   func(r *OrderRepository) // pre-populate
		wantErr bool
		errCode errcode.Code
	}{
		{
			name:  "success",
			order: &domain.Order{ID: "ord-1", Item: "widget", Status: "pending"},
		},
		{
			name:  "duplicate ID returns error",
			order: &domain.Order{ID: "ord-dup", Item: "gadget", Status: "pending"},
			setup: func(r *OrderRepository) {
				_ = r.Create(context.Background(), &domain.Order{ID: "ord-dup", Item: "first", Status: "pending"})
			},
			wantErr: true,
			errCode: errcode.ErrValidationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewOrderRepository()
			if tt.setup != nil {
				tt.setup(repo)
			}

			err := repo.Create(context.Background(), tt.order)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.ErrorAs(t, err, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOrderRepository_Create_StoresCopy(t *testing.T) {
	repo := NewOrderRepository()
	order := &domain.Order{ID: "ord-copy", Item: "original", Status: "pending"}
	require.NoError(t, repo.Create(context.Background(), order))

	// Mutate the original struct; stored value should be unaffected.
	order.Item = "mutated"

	got, err := repo.GetByID(context.Background(), "ord-copy")
	require.NoError(t, err)
	assert.Equal(t, "original", got.Item, "repository should store a copy")
}

func TestOrderRepository_GetByID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		setup   func(r *OrderRepository)
		wantErr bool
		errCode errcode.Code
	}{
		{
			name: "found",
			id:   "ord-found",
			setup: func(r *OrderRepository) {
				_ = r.Create(context.Background(), &domain.Order{ID: "ord-found", Item: "x", Status: "pending"})
			},
		},
		{
			name:    "not found",
			id:      "ord-missing",
			wantErr: true,
			errCode: errcode.ErrOrderNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewOrderRepository()
			if tt.setup != nil {
				tt.setup(repo)
			}

			got, err := repo.GetByID(context.Background(), tt.id)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.ErrorAs(t, err, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.id, got.ID)
			}
		})
	}
}

func TestOrderRepository_GetByID_ReturnsCopy(t *testing.T) {
	repo := NewOrderRepository()
	_ = repo.Create(context.Background(), &domain.Order{ID: "ord-rc", Item: "item", Status: "pending"})

	got, err := repo.GetByID(context.Background(), "ord-rc")
	require.NoError(t, err)
	got.Item = "mutated"

	got2, err := repo.GetByID(context.Background(), "ord-rc")
	require.NoError(t, err)
	assert.Equal(t, "item", got2.Item, "should return a copy, not internal pointer")
}

var defaultSort = []query.SortColumn{
	{Name: "created_at", Direction: "DESC"},
	{Name: "id", Direction: "ASC"},
}

func TestOrderRepository_List(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(r *OrderRepository)
		wantCount int
	}{
		{
			name:      "empty",
			wantCount: 0,
		},
		{
			name: "multiple orders",
			setup: func(r *OrderRepository) {
				_ = r.Create(context.Background(), &domain.Order{ID: "ord-a", Item: "a"})
				_ = r.Create(context.Background(), &domain.Order{ID: "ord-b", Item: "b"})
				_ = r.Create(context.Background(), &domain.Order{ID: "ord-c", Item: "c"})
			},
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewOrderRepository()
			if tt.setup != nil {
				tt.setup(repo)
			}

			params := query.ListParams{Limit: 100, Sort: defaultSort}
			orders, err := repo.List(context.Background(), params)
			require.NoError(t, err)
			assert.Len(t, orders, tt.wantCount)
		})
	}
}

func TestOrderRepository_ListPaged_FirstPage(t *testing.T) {
	repo := NewOrderRepository()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		_ = repo.Create(context.Background(), &domain.Order{
			ID:        fmt.Sprintf("ord-%02d", i),
			Item:      fmt.Sprintf("item-%d", i),
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}

	params := query.ListParams{
		Limit: 3,
		Sort:  defaultSort,
	}
	orders, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	// FetchLimit = 3+1 = 4
	assert.Len(t, orders, 4)
	// DESC by created_at: newest first (ord-09, ord-08, ord-07, ord-06)
	assert.Equal(t, "ord-09", orders[0].ID)
	assert.Equal(t, "ord-08", orders[1].ID)
}

func TestOrderRepository_ListPaged_WithCursor(t *testing.T) {
	repo := NewOrderRepository()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		_ = repo.Create(context.Background(), &domain.Order{
			ID:        fmt.Sprintf("ord-%02d", i),
			Item:      fmt.Sprintf("item-%d", i),
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}

	// Simulate cursor after ord-07 (created_at = base+7h)
	cursorTime := base.Add(7 * time.Hour).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        3,
		CursorValues: []any{cursorTime, "ord-07"},
		Sort:         defaultSort,
	}
	orders, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	// After ord-07 (DESC): ord-06, ord-05, ord-04, ord-03 (4 = limit+1)
	assert.Len(t, orders, 4)
	assert.Equal(t, "ord-06", orders[0].ID)
	assert.Equal(t, "ord-05", orders[1].ID)
}

func TestOrderRepository_ListPaged_LastPage(t *testing.T) {
	repo := NewOrderRepository()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = repo.Create(context.Background(), &domain.Order{
			ID:        fmt.Sprintf("ord-%02d", i),
			Item:      fmt.Sprintf("item-%d", i),
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}

	// Cursor after ord-01 (DESC): only ord-00 left
	cursorTime := base.Add(1 * time.Hour).Format(time.RFC3339Nano)
	params := query.ListParams{
		Limit:        3,
		CursorValues: []any{cursorTime, "ord-01"},
		Sort:         defaultSort,
	}
	orders, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	// Only 1 item left, less than FetchLimit(4) → last page
	assert.Len(t, orders, 1)
	assert.Equal(t, "ord-00", orders[0].ID)
}

func TestOrderRepository_ListPaged_Empty(t *testing.T) {
	repo := NewOrderRepository()
	params := query.ListParams{Limit: 10, Sort: defaultSort}
	orders, err := repo.List(context.Background(), params)
	require.NoError(t, err)
	assert.Empty(t, orders)
}
