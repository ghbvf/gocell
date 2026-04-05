package mem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
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
			errCode: errcode.ErrCellNotFound,
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

			orders, err := repo.List(context.Background())
			require.NoError(t, err)
			assert.Len(t, orders, tt.wantCount)
		})
	}
}
