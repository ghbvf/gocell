package orderquery

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func seedRepo(orders ...*domain.Order) *mem.OrderRepository {
	repo := mem.NewOrderRepository()
	for _, o := range orders {
		_ = repo.Create(context.Background(), o)
	}
	return repo
}

func TestService_GetByID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		seed    []*domain.Order
		wantErr bool
		errCode errcode.Code
	}{
		{
			name: "found",
			id:   "ord-1",
			seed: []*domain.Order{{ID: "ord-1", Item: "widget", Status: "pending"}},
		},
		{
			name:    "not found",
			id:      "ord-missing",
			seed:    nil,
			wantErr: true,
			errCode: errcode.ErrCellNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := seedRepo(tt.seed...)
			svc := NewService(repo, slog.Default())

			order, err := svc.GetByID(context.Background(), tt.id)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.ErrorAs(t, err, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
				assert.Nil(t, order)
			} else {
				require.NoError(t, err)
				require.NotNil(t, order)
				assert.Equal(t, tt.id, order.ID)
			}
		})
	}
}

func TestService_List(t *testing.T) {
	tests := []struct {
		name      string
		seed      []*domain.Order
		wantCount int
	}{
		{
			name:      "empty",
			seed:      nil,
			wantCount: 0,
		},
		{
			name: "returns all orders",
			seed: []*domain.Order{
				{ID: "ord-a", Item: "a", Status: "pending"},
				{ID: "ord-b", Item: "b", Status: "confirmed"},
			},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := seedRepo(tt.seed...)
			svc := NewService(repo, slog.Default())

			orders, err := svc.List(context.Background())
			require.NoError(t, err)
			assert.Len(t, orders, tt.wantCount)
		})
	}
}
