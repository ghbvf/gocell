package transport_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// assertKindInternal is a test helper that unwraps err to *errcode.Error and
// asserts its Kind == KindInternal.
func assertKindInternal(t *testing.T, err error) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInternal {
		t.Errorf("Kind = %v, want KindInternal", ec.Kind)
	}
}

// TestStaticResolver_ResolvesRemoteEndpoint verifies that a cell present in the
// endpoint map resolves to its endpoint.
func TestStaticResolver_ResolvesRemoteEndpoint(t *testing.T) {
	t.Parallel()

	r := transport.NewStaticResolver(map[string]string{
		"configcore": "127.0.0.1:9090",
	})
	got, err := r.Resolve(context.Background(), "configcore")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "127.0.0.1:9090" {
		t.Errorf("Resolve = %q, want %q", got, "127.0.0.1:9090")
	}
}

// TestStaticResolver_UnknownCellReturnsKindInternal verifies that resolving a
// cell not in the endpoint map yields KindInternal (wiring error, not transient).
func TestStaticResolver_UnknownCellReturnsKindInternal(t *testing.T) {
	t.Parallel()

	r := transport.NewStaticResolver(map[string]string{
		"configcore": "127.0.0.1:9090",
	})
	_, resolveErr := r.Resolve(context.Background(), "unknowncell")
	if resolveErr == nil {
		t.Fatal("expected error for unknown cell, got nil")
	}
	assertKindInternal(t, resolveErr)
}

// TestStaticResolver_NilMapAlwaysReturnsKindInternal verifies that a nil map
// (no remote cells) returns KindInternal for any cellID.
func TestStaticResolver_NilMapAlwaysReturnsKindInternal(t *testing.T) {
	t.Parallel()

	r := transport.NewStaticResolver(nil)
	_, resolveErr := r.Resolve(context.Background(), "configcore")
	if resolveErr == nil {
		t.Fatal("expected error for nil map, got nil")
	}
	assertKindInternal(t, resolveErr)
}

// TestStaticResolver_MapIsCopied verifies that mutating the source map after
// construction does not affect the resolver.
func TestStaticResolver_MapIsCopied(t *testing.T) {
	t.Parallel()

	m := map[string]string{"configcore": "127.0.0.1:9090"}
	r := transport.NewStaticResolver(m)
	delete(m, "configcore")

	got, err := r.Resolve(context.Background(), "configcore")
	if err != nil {
		t.Fatalf("Resolve after source delete: %v", err)
	}
	if got != "127.0.0.1:9090" {
		t.Errorf("Resolve = %q, want %q", got, "127.0.0.1:9090")
	}
}

// compile-time interface check.
var _ transport.Resolver = (*transport.StaticResolver)(nil)
