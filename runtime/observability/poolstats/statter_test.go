package poolstats_test

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/poolstats"
)

func TestSnapshot_ZeroValueIsValid(t *testing.T) {
	// A zero-value Snapshot represents an uninitialised pool (no
	// connections established yet). All fields zero is a legitimate
	// reading, not a sentinel for "unknown".
	var s poolstats.Snapshot
	if s.TotalConns != 0 || s.IdleConns != 0 || s.UsedConns != 0 {
		t.Fatalf("zero-value Snapshot must be all zero, got %+v", s)
	}
}

// staticStatter exercises the Statter interface shape — if the interface
// grows new methods, this compile-time check flags the breakage.
type staticStatter struct {
	name string
	snap poolstats.Snapshot
}

func (s staticStatter) PoolName() string           { return s.name }
func (s staticStatter) Snapshot() poolstats.Snapshot { return s.snap }

var _ poolstats.Statter = staticStatter{}

func TestStatter_InterfaceShape(t *testing.T) {
	s := staticStatter{name: "test", snap: poolstats.Snapshot{TotalConns: 5, IdleConns: 3, UsedConns: 2}}
	if s.PoolName() != "test" {
		t.Fatalf("PoolName = %q, want test", s.PoolName())
	}
	if s.Snapshot().UsedConns != 2 {
		t.Fatalf("UsedConns = %d, want 2", s.Snapshot().UsedConns)
	}
}
