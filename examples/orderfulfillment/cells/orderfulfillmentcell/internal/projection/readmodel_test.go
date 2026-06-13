package projection_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/projection"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
)

func TestMemReadModel_UpsertGet(t *testing.T) {
	t.Parallel()
	rm := projection.NewMemReadModel()
	ctx := context.Background()

	// Get before any upsert returns not-found.
	_, found, err := rm.Get(ctx, "ord-1")
	if err != nil {
		t.Fatalf("Get before upsert: %v", err)
	}
	if found {
		t.Fatal("Get before upsert: found=true, want false")
	}

	// Upsert and then Get returns the stored status.
	if err := rm.Upsert(ctx, "ord-1", orderstatusgen.ResponseDataStatusRunning); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	status, found, err := rm.Get(ctx, "ord-1")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if !found {
		t.Fatal("Get after upsert: found=false, want true")
	}
	if status != orderstatusgen.ResponseDataStatusRunning {
		t.Errorf("Get = %q, want %q", status, orderstatusgen.ResponseDataStatusRunning)
	}

	// Upsert again with a different status overwrites.
	if err := rm.Upsert(ctx, "ord-1", orderstatusgen.ResponseDataStatusSucceeded); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	status2, found2, err := rm.Get(ctx, "ord-1")
	if err != nil {
		t.Fatalf("Get after second upsert: %v", err)
	}
	if !found2 {
		t.Fatal("Get after second upsert: found=false")
	}
	if status2 != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("Get = %q, want %q", status2, orderstatusgen.ResponseDataStatusSucceeded)
	}
}

func TestMemReadModel_MultiplOrders(t *testing.T) {
	t.Parallel()
	rm := projection.NewMemReadModel()
	ctx := context.Background()

	// Each order ID has its own slot.
	if err := rm.Upsert(ctx, "ord-A", orderstatusgen.ResponseDataStatusRunning); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := rm.Upsert(ctx, "ord-B", orderstatusgen.ResponseDataStatusSucceeded); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}

	stA, foundA, _ := rm.Get(ctx, "ord-A")
	stB, foundB, _ := rm.Get(ctx, "ord-B")
	_, foundC, _ := rm.Get(ctx, "ord-C")

	if !foundA || stA != orderstatusgen.ResponseDataStatusRunning {
		t.Errorf("ord-A: found=%v status=%q, want true/running", foundA, stA)
	}
	if !foundB || stB != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("ord-B: found=%v status=%q, want true/succeeded", foundB, stB)
	}
	if foundC {
		t.Error("ord-C: found=true, want false")
	}
}
