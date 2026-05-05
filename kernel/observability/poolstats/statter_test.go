package poolstats_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/observability/poolstats"
)

func TestSnapshot_ZeroValueIsValid(t *testing.T) {
	// A zero-value Snapshot represents an uninitialised pool (no
	// connections established yet). All fields zero is a legitimate
	// reading, not a sentinel for "unknown". Struct equality covers
	// every field so adding a new field automatically extends this
	// invariant rather than silently slipping through.
	var s poolstats.Snapshot
	if s != (poolstats.Snapshot{}) {
		t.Fatalf("zero-value Snapshot must be all zero, got %+v", s)
	}
}

// staticStatter exercises the Statter interface shape — if the interface
// grows new methods, this compile-time check flags the breakage.
type staticStatter struct {
	name string
	snap poolstats.Snapshot
}

func (s staticStatter) PoolName() string             { return s.name }
func (s staticStatter) Snapshot() poolstats.Snapshot { return s.snap }

var _ poolstats.Statter = staticStatter{}

func TestStatter_InterfaceShape(t *testing.T) {
	want := poolstats.Snapshot{TotalConns: 5, IdleConns: 3, UsedConns: 2, MaxConns: 10, WaitCount: 7}
	s := staticStatter{name: "test", snap: want}
	if s.PoolName() != "test" {
		t.Fatalf("PoolName = %q, want test", s.PoolName())
	}
	if got := s.Snapshot(); got != want {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}
}
