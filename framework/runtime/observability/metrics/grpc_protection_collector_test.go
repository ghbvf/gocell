package metrics

import (
	"context"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// TestProtectionRateLimit verifies the ProtectionRateLimit accessor.
func TestProtectionRateLimit(t *testing.T) {
	pt := ProtectionRateLimit()
	if got := pt.String(); got != "ratelimit" {
		t.Errorf("ProtectionRateLimit().String() = %q, want %q", got, "ratelimit")
	}
}

// TestProtectionCircuit verifies the ProtectionCircuit accessor.
func TestProtectionCircuit(t *testing.T) {
	pt := ProtectionCircuit()
	if got := pt.String(); got != "circuit" {
		t.Errorf("ProtectionCircuit().String() = %q, want %q", got, "circuit")
	}
}

// TestProtectionTypeDistinct verifies the two types are distinct values.
func TestProtectionTypeDistinct(t *testing.T) {
	if ProtectionRateLimit().String() == ProtectionCircuit().String() {
		t.Error("ProtectionRateLimit and ProtectionCircuit must be distinct values")
	}
}

// TestInMemoryGRPCCollector_RecordProtectionRejection verifies the in-memory
// collector counts protection rejections by type/method/cell.
func TestInMemoryGRPCCollector_RecordProtectionRejection(t *testing.T) {
	c := NewInMemoryGRPCCollector()
	ctx := context.Background()

	// ratelimit deny on /pkg.Svc/Do for _runtime cell.
	c.RecordProtectionRejection(ctx, testLabel(""), "/pkg.Svc/Do", ProtectionRateLimit())
	c.RecordProtectionRejection(ctx, testLabel(""), "/pkg.Svc/Do", ProtectionRateLimit())
	// circuit open on different method.
	c.RecordProtectionRejection(ctx, testLabel(""), "/pkg.Svc/Other", ProtectionCircuit())

	if got := c.ProtectionCount("ratelimit", "/pkg.Svc/Do", "_runtime"); got != 2 {
		t.Errorf("ratelimit /pkg.Svc/Do count = %d, want 2", got)
	}
	if got := c.ProtectionCount("circuit", "/pkg.Svc/Other", "_runtime"); got != 1 {
		t.Errorf("circuit /pkg.Svc/Other count = %d, want 1", got)
	}
	// Absent key → 0.
	if got := c.ProtectionCount("ratelimit", "/pkg.Svc/Other", "_runtime"); got != 0 {
		t.Errorf("absent key count = %d, want 0", got)
	}
}

// TestNewGRPCProviderCollector_RecordProtectionRejection verifies the provider
// collector registers grpc_protection_rejected_total without error and records
// without panicking.
func TestNewGRPCProviderCollector_RecordProtectionRejection(t *testing.T) {
	p := kernelmetrics.NopProvider{}
	c, err := NewGRPCProviderCollector(p, ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewGRPCProviderCollector err = %v", err)
	}
	// Must not panic.
	c.RecordProtectionRejection(context.Background(), testLabel(""), "/pkg.Svc/Do", ProtectionRateLimit())
	c.RecordProtectionRejection(context.Background(), testLabel(""), "/pkg.Svc/Do", ProtectionCircuit())
}
