package orderstatus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func newService(t *testing.T) (*orderstatus.Service, *mem.OrderRepository) {
	t.Helper()
	clk := clock.Real()
	repo := mem.NewOrderRepository()
	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	svc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(repo),
		orderstatus.WithJournal(jrnl),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, repo
}

func TestGetOrderStatus_NotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	_, err := svc.GetOrderStatus(context.Background(), "ord-nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent order, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindNotFound {
		t.Errorf("expected KindNotFound, got %v", err)
	}
}

func TestGetOrderStatus_Accepted(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	order := &domain.Order{ID: "ord-1", Item: "widget", AmountCents: 1000, CreatedAt: time.Now()}
	if err := repo.Create(context.Background(), order); err != nil {
		t.Fatalf("Create: %v", err)
	}
	status, err := svc.GetOrderStatus(context.Background(), "ord-1")
	if err != nil {
		t.Fatalf("GetOrderStatus: %v", err)
	}
	if status != orderstatus.StatusAccepted {
		t.Errorf("status = %q, want %q", status, orderstatus.StatusAccepted)
	}
}

func TestNewService_MissingDeps(t *testing.T) {
	t.Parallel()
	clk := clock.Real()

	t.Run("missing_repo", func(t *testing.T) {
		t.Parallel()
		jrnl, err := journal.NewMemJournal(clk)
		if err != nil {
			t.Fatalf("NewMemJournal: %v", err)
		}
		_, err = orderstatus.NewService(clk, orderstatus.WithJournal(jrnl))
		if err == nil {
			t.Fatal("expected error for missing repo, got nil")
		}
	})

	t.Run("missing_journal", func(t *testing.T) {
		t.Parallel()
		_, err := orderstatus.NewService(clk, orderstatus.WithOrderRepository(mem.NewOrderRepository()))
		if err == nil {
			t.Fatal("expected error for missing journal, got nil")
		}
	})
}
