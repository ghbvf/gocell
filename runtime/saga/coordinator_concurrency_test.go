package saga

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// concProbe builds a Coordinator whose single saga step blocks on a release
// channel, instrumenting how many drives run concurrently within one tick.
// It exercises the bounded-concurrent tickOnce fan-out (#983).
type concProbe struct {
	c        *Coordinator
	release  chan struct{}
	relOnce  sync.Once
	inFlight atomic.Int64 // currently inside the step body
	peak     atomic.Int64 // max inFlight ever observed
	entered  atomic.Int64 // total step entries (all instances eventually driven)
}

// releaseAll unblocks every step. Idempotent (safe to defer + call explicitly).
func (p *concProbe) releaseAll() { p.relOnce.Do(func() { close(p.release) }) }

// newConcurrencyProbe wires a single-process Coordinator with MaxConcurrentDrives
// = maxConcurrent and seeds nInstances pending saga instances. The step records
// concurrency then blocks until releaseAll().
func newConcurrencyProbe(t *testing.T, maxConcurrent, nInstances int) *concProbe {
	t.Helper()
	const defID idutil.SafeID = "tickconcurrency"
	p := &concProbe{release: make(chan struct{})}

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "concstep",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					cur := p.inFlight.Add(1)
					p.entered.Add(1)
					for { // CAS running peak
						old := p.peak.Load()
						if cur <= old || p.peak.CompareAndSwap(old, cur) {
							break
						}
					}
					defer p.inFlight.Add(-1)
					select {
					case <-p.release:
						return []byte(`{}`), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			},
		},
	}

	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	cfg := DefaultConfig()
	cfg.MaxConcurrentDrives = maxConcurrent
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk, WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	p.c = c

	for i := 0; i < nInstances; i++ {
		inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
		if err := j.Enqueue(context.Background(), inst); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	return p
}

// TestTickOnce_DrivesConcurrently asserts a claimed batch drives concurrently:
// with MaxConcurrentDrives ≥ batch, all N instances are inside their step body
// at the same time. Under the previous serial loop only one driveOne ran at a
// time, so peak would never reach N and the testwait below would time out — this
// test is red without the fan-out.
func TestTickOnce_DrivesConcurrently(t *testing.T) {
	const n = 4
	p := newConcurrencyProbe(t, 16, n) // 16 = default, ≥ n
	defer p.releaseAll()

	ctx := context.Background()
	tickDone := make(chan error, 1)
	go func() { tickDone <- p.c.tickOnce(ctx) }()

	testwait.External(t, "all-drives-concurrent",
		func() bool { return p.peak.Load() >= int64(n) },
		testtime.D2s, testtime.D1ms)

	p.releaseAll()
	if err := testwait.Deterministic(t, tickDone, "tickOnce-returned"); err != nil {
		t.Errorf("tickOnce returned error: %v", err)
	}
	if got := p.peak.Load(); got != int64(n) {
		t.Errorf("peak concurrency = %d, want %d", got, n)
	}
	if got := p.entered.Load(); got != int64(n) {
		t.Errorf("entered = %d, want %d", got, n)
	}
}

// TestTickOnce_BoundedByMaxConcurrentDrives asserts the semaphore caps in-flight
// drives: with MaxConcurrentDrives=2 and 4 claimed instances, at most 2 steps
// run at once (the other 2 block on the semaphore) while all 4 are eventually
// driven within the single tick.
func TestTickOnce_BoundedByMaxConcurrentDrives(t *testing.T) {
	const (
		maxConcurrent = 2
		n             = 4
	)
	p := newConcurrencyProbe(t, maxConcurrent, n)
	defer p.releaseAll()

	ctx := context.Background()
	tickDone := make(chan error, 1)
	go func() { tickDone <- p.c.tickOnce(ctx) }()

	// Both semaphore slots fill; the remaining 2 instances block on the semaphore.
	testwait.External(t, "slots-filled",
		func() bool { return p.peak.Load() >= int64(maxConcurrent) },
		testtime.D2s, testtime.D1ms)

	p.releaseAll()
	if err := testwait.Deterministic(t, tickDone, "tickOnce-returned"); err != nil {
		t.Errorf("tickOnce returned error: %v", err)
	}
	if got := p.peak.Load(); got != int64(maxConcurrent) {
		t.Errorf("peak concurrency = %d, want exactly %d (semaphore bound)", got, maxConcurrent)
	}
	if got := p.entered.Load(); got != int64(n) {
		t.Errorf("entered = %d, want %d (all instances eventually driven)", got, n)
	}
}
