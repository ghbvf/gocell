package sagaprojectiondeps_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cellmodules/sagaprojectiondeps"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

func mkTopo(t *testing.T, adapterMode, storageBackend string, singlePod bool) bootstrap.Topology {
	t.Helper()
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, singlePod)
	if err != nil {
		t.Fatalf("NewTopology(%q,%q,%v): %v", adapterMode, storageBackend, singlePod, err)
	}
	return topo
}

func newClk() *clockmock.FakeClock {
	return clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// TestResolve_DemoMemory verifies the demo/memory branch resolves a complete,
// in-process saga-projection dependency set: a MemJournal usable as both the
// Coordinator journal and the projection GlobalReader, an in-memory owner
// checkpoint store, an in-process leader locker, a demo TxRunner, and no managed
// resources (nothing to close).
func TestResolve_DemoMemory(t *testing.T) {
	t.Parallel()
	deps, err := sagaprojectiondeps.Resolve(context.Background(), newClk(),
		mkTopo(t, "", "memory", false), sagaprojectiondeps.Config{})
	if err != nil {
		t.Fatalf("Resolve(demo): %v", err)
	}
	if deps.Journal == nil {
		t.Fatal("demo Journal is nil")
	}
	if _, ok := deps.Journal.(journal.GlobalReader); !ok {
		t.Errorf("demo Journal %T does not implement journal.GlobalReader", deps.Journal)
	}
	if deps.Reader == nil {
		t.Fatal("demo Reader is nil")
	}
	if deps.OwnerStore == nil {
		t.Fatal("demo OwnerStore is nil")
	}
	if deps.Locker == nil {
		t.Fatal("demo Locker is nil (Tailer requires a non-nil distlock.Locker)")
	}
	if deps.TxRunner == nil {
		t.Fatal("demo TxRunner is nil")
	}
}

// TestResolve_PostgresSinglePodNilPool fail-closes on the POOL gate: postgres,
// single-pod topology resolves an in-process locker fine (single-pod → no Redis
// needed), so the only remaining fail-close is the missing PG pool. Asserts the
// pool-specific fail-closed error — never a silent degrade to an in-memory
// journal (which would lose events across restart — the durability the postgres
// topology promises).
func TestResolve_PostgresSinglePodNilPool(t *testing.T) {
	t.Parallel()
	_, err := sagaprojectiondeps.Resolve(context.Background(), newClk(),
		mkTopo(t, "real", "postgres", true), sagaprojectiondeps.Config{Pool: nil})
	if err == nil {
		t.Fatal("Resolve(postgres, single-pod, nil pool) = nil error, want fail-closed startup error")
	}
	if !strings.Contains(err.Error(), "requires a PG pool") {
		t.Errorf("error = %q, want the POOL fail-closed message (single-pod hits the pool gate, not the Redis gate)", err)
	}
}

// TestResolve_MultiPodNilRedis fail-closes on the REDIS gate FIRST: real
// multi-pod topology requires a Redis-backed leader locker; an in-process locker
// cannot coordinate leadership across replicas. Because Resolve runs the locker
// gate before the store gate, a config that is missing BOTH the pool and the
// Redis client reports the Redis fail-closed error (the locker gate fires first),
// proving the Redis gate is reached and is not masked by the pool gate (F9
// false-green: a nil-pool-first ordering would never exercise the Redis gate).
func TestResolve_MultiPodNilRedis(t *testing.T) {
	t.Parallel()
	_, err := sagaprojectiondeps.Resolve(context.Background(), newClk(),
		mkTopo(t, "real", "postgres", false), sagaprojectiondeps.Config{Pool: nil, RedisClient: nil})
	if err == nil {
		t.Fatal("Resolve(multi-pod, nil redis) = nil error, want fail-closed startup error")
	}
	if !strings.Contains(err.Error(), "requires a Redis-backed projection") {
		t.Errorf("error = %q, want the REDIS fail-closed message (locker gate must fire before the pool gate)", err)
	}
}
