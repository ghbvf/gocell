package orderquery

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

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
			errCode: errcode.ErrOrderNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := seedRepo(tt.seed...)
			svc := NewService(repo, testCodec(), slog.Default())

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

func TestService_List_FirstPage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var seed []*domain.Order
	for i := 0; i < 5; i++ {
		seed = append(seed, &domain.Order{
			ID:        fmt.Sprintf("ord-%02d", i),
			Item:      "item",
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	repo := seedRepo(seed...)
	svc := NewService(repo, testCodec(), slog.Default())

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.True(t, result.HasMore)
	assert.NotEmpty(t, result.NextCursor)
	// DESC: newest first
	assert.Equal(t, "ord-04", result.Items[0].ID)
}

func TestService_List_WithCursor(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var seed []*domain.Order
	for i := 0; i < 10; i++ {
		seed = append(seed, &domain.Order{
			ID:        fmt.Sprintf("ord-%02d", i),
			Item:      "item",
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	repo := seedRepo(seed...)
	svc := NewService(repo, testCodec(), slog.Default())

	// Get first page
	page1, err := svc.List(context.Background(), query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)

	// Get second page using cursor
	page2, err := svc.List(context.Background(), query.PageRequest{Limit: 3, Cursor: page1.NextCursor})
	require.NoError(t, err)
	assert.Len(t, page2.Items, 3)
	// Second page should continue where first left off
	assert.NotEqual(t, page1.Items[0].ID, page2.Items[0].ID)
}

func TestService_List_InvalidCursor(t *testing.T) {
	repo := seedRepo()
	svc := NewService(repo, testCodec(), slog.Default())

	_, err := svc.List(context.Background(), query.PageRequest{Cursor: "garbage-token"})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestService_List_LastPage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := []*domain.Order{
		{ID: "ord-00", Item: "a", Status: "pending", CreatedAt: base},
		{ID: "ord-01", Item: "b", Status: "pending", CreatedAt: base.Add(time.Hour)},
	}
	repo := seedRepo(seed...)
	svc := NewService(repo, testCodec(), slog.Default())

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_List_Empty(t *testing.T) {
	repo := seedRepo()
	svc := NewService(repo, testCodec(), slog.Default())

	result, err := svc.List(context.Background(), query.PageRequest{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}
