package postgres

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/poolstats"
)

func TestPool_Statter_NilPool_ReturnsZeroSnapshot(t *testing.T) {
	// A Pool with nil inner (constructor failure path) must not panic —
	// the OTel collector calls Snapshot() on every observable-gauge
	// callback and a panic would propagate into the OTel SDK.
	p := &Pool{}
	s := p.Statter("pg-nil")
	if s.PoolName() != "pg-nil" {
		t.Fatalf("PoolName = %q, want pg-nil", s.PoolName())
	}
	snap := s.Snapshot()
	if (snap != poolstats.Snapshot{}) {
		t.Fatalf("nil pool must yield zero-value snapshot, got %+v", snap)
	}
}

// Compile-time assertion that *Pool.Statter satisfies Statter.
var _ poolstats.Statter = (*pgPoolStatter)(nil)
