package lifecycle_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
)

type fakeResource struct{}

func (fakeResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{"fake": func(_ context.Context) error { return nil }}
}
func (fakeResource) Worker() worker.Worker         { return nil }
func (fakeResource) Close(_ context.Context) error { return nil }

var _ lifecycle.ManagedResource = fakeResource{}

func TestManagedResource_InterfaceContract(t *testing.T) {
	var r lifecycle.ManagedResource = fakeResource{}
	if len(r.Checkers()) != 1 {
		t.Errorf("expected 1 checker, got %d", len(r.Checkers()))
	}
	if r.Worker() != nil {
		t.Errorf("expected nil worker")
	}
	if err := r.Close(context.Background()); err != nil {
		t.Errorf("unexpected Close error: %v", err)
	}
}
