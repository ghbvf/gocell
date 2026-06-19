package registryadmin

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
)

// TestNewService_NilStore: store is a gocell:"required" dep, so validateRequired
// (generated in service_required_gen.go) must fail-fast on a nil store.
func TestNewService_NilStore(t *testing.T) {
	if _, err := NewService(nil, WithTxManager(persistence.WrapForCell(noopTxRunner{}))); err == nil {
		t.Fatal("NewService(nil store) must fail-fast — store is gocell:\"required\"")
	}
}

// TestNewService_NilTxRunner: txRunner is a gocell:"required" dep; omitting
// WithTxManager leaves it nil, so NewService must fail-fast.
func TestNewService_NilTxRunner(t *testing.T) {
	store := mem.NewRegistry(clockmock.New(testEpoch))
	if _, err := NewService(store); err == nil {
		t.Fatal("NewService without WithTxManager must fail-fast — txRunner is gocell:\"required\"")
	}
}

// TestNewService_OK: with both required deps wired, NewService succeeds.
func TestNewService_OK(t *testing.T) {
	store := mem.NewRegistry(clockmock.New(testEpoch))
	if _, err := NewService(store, WithTxManager(persistence.WrapForCell(noopTxRunner{}))); err != nil {
		t.Fatalf("NewService with all required deps must succeed, got: %v", err)
	}
}
