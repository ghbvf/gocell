package interceptor

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// funnelDeps is the Deps shape callers supply post-#1752: NO Registrar/Drain —
// NewServerInterceptors mints the single shared pair internally so the chains and
// the adapter binding can never observe two different instances.
func funnelDeps() Deps {
	return Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		CellIDClosedSet: []string{"svc-cell"},
	}
}

// TestNewServerInterceptors_MintsRegistrarAndDrain asserts the funnel mints a
// valid registrar + drain from a Deps that carries neither — the compile-proof
// same-instance guarantee (#1752): the caller can no longer supply (and therefore
// can no longer mismatch) the registrar/drain.
func TestNewServerInterceptors_MintsRegistrarAndDrain(t *testing.T) {
	b := NewServerInterceptors(funnelDeps())
	if err := b.Validate(); err != nil {
		t.Fatalf("bundle from a registrar/drain-free Deps must be valid, got %v", err)
	}
	if b.Registrar() == nil {
		t.Fatalf("NewServerInterceptors must mint a registrar")
	}
	if err := b.Drain().Validate(); err != nil {
		t.Fatalf("NewServerInterceptors must mint a valid drain, got %v", err)
	}
}

// TestNewServerInterceptors_MintsFreshPerCall asserts each funnel call mints its
// own registrar/drain pair — two servers built from the same Deps value never
// share an attribution map or drain signal.
//
// The end-to-end same-instance proof (a method registered on the bundle's
// registrar is attributed by the chain the adapter runs) lives in
// adapters/grpc/server_integration_test.go, which drives a real RPC through a
// server built from this bundle.
func TestNewServerInterceptors_MintsFreshPerCall(t *testing.T) {
	a := NewServerInterceptors(funnelDeps())
	c := NewServerInterceptors(funnelDeps())
	if a.Registrar() == c.Registrar() {
		t.Fatalf("each NewServerInterceptors call must mint a fresh registrar")
	}
	if a.Drain() == c.Drain() {
		t.Fatalf("each NewServerInterceptors call must mint a fresh drain")
	}
}
