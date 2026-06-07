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
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// concProbe builds a Coordinator whose single saga step blocks on a release
// channel, instrumenting how many drives run concurrently within one tick.
// It exercises the concurrent tickOnce fan-out (#983).
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

// newConcurrencyProbe wires a single-process Coordinator and seeds nInstances
// pending saga instances. The step records concurrency then blocks until
// releaseAll(). Extra opts are applied AFTER the DefaultConfig baseline, so a
// caller may override ClaimBatchSize (the concurrency bound) or attach an
// Observer.
func newConcurrencyProbe(t *testing.T, nInstances int, opts ...Option) *concProbe {
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
	ctorOpts := append([]Option{WithConfig(DefaultConfig())}, opts...)
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk, ctorOpts...)
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
// with ClaimBatchSize (default 16) ≥ batch, all N instances are inside their
// step body at the same time. Under the previous serial loop only one driveOne
// ran at a time, so peak would never reach N and the testwait below would time
// out — this test is red without the fan-out.
func TestTickOnce_DrivesConcurrently(t *testing.T) {
	t.Parallel()
	const n = 4
	p := newConcurrencyProbe(t, n) // default ClaimBatchSize=16 ≥ n
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

// TestTickOnce_ConcurrencyBoundedByClaimBatchSize asserts ClaimBatchSize is the
// per-tick concurrency bound: with ClaimBatchSize=4 and 8 pending instances, a
// single tick claims and concurrently drives exactly 4 (peak==4, entered==4) —
// the remaining 4 stay pending for a later tick. After #983 deleted the separate
// MaxConcurrentDrives knob, ClaimBatchSize alone bounds drive fan-out because a
// claimed instance must drive immediately (its lease is only kept alive by the
// heartbeat that starts inside driveOne), so claim count == concurrency.
func TestTickOnce_ConcurrencyBoundedByClaimBatchSize(t *testing.T) {
	t.Parallel()
	const (
		batch = 4
		n     = 8
	)
	cfg := DefaultConfig()
	cfg.ClaimBatchSize = batch
	p := newConcurrencyProbe(t, n, WithConfig(cfg))
	defer p.releaseAll()

	ctx := context.Background()
	tickDone := make(chan error, 1)
	go func() { tickDone <- p.c.tickOnce(ctx) }()

	// All batch slots fill concurrently; the remaining instances are not claimed
	// this tick (no parked-without-heartbeat leases — that was the #983 bug).
	testwait.External(t, "batch-driven-concurrently",
		func() bool { return p.peak.Load() >= int64(batch) },
		testtime.D2s, testtime.D1ms)

	p.releaseAll()
	if err := testwait.Deterministic(t, tickDone, "tickOnce-returned"); err != nil {
		t.Errorf("tickOnce returned error: %v", err)
	}
	if got := p.peak.Load(); got != int64(batch) {
		t.Errorf("peak concurrency = %d, want exactly %d (ClaimBatchSize bound)", got, batch)
	}
	if got := p.entered.Load(); got != int64(batch) {
		t.Errorf("entered = %d, want %d (only one batch claimed per tick)", got, batch)
	}
}

// recordingDriveObserver is a concurrency-safe executor.Observer that counts
// ObserveDrive calls. It embeds NopObserver and overrides only ObserveDrive.
type recordingDriveObserver struct {
	executor.NopObserver
	mu     sync.Mutex
	drives []executor.DriveResult
}

func (o *recordingDriveObserver) ObserveDrive(_ context.Context, _ string, result executor.DriveResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.drives = append(o.drives, result)
}

func (o *recordingDriveObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.drives)
}

// TestObserveDrive_ConcurrentCallsRaceSafe exercises the concurrent-invocation
// contract documented on executor.Observer (#1714 F2): a tick drives its claimed
// instances in parallel, so ObserveDrive fires from multiple goroutines at once.
// Run under `go test -race ./runtime/saga/...` (PR CI), a data race in the
// coordinator's observer-call path — or in a non-thread-safe Observer — reports
// red. The recording observer is mutex-guarded; the assertion confirms every
// concurrent drive reached the sink exactly once.
func TestObserveDrive_ConcurrentCallsRaceSafe(t *testing.T) {
	t.Parallel()
	const n = 8
	rec := &recordingDriveObserver{}
	p := newConcurrencyProbe(t, n, WithObserver(rec)) // default ClaimBatchSize=16 ≥ n
	defer p.releaseAll()

	ctx := context.Background()
	tickDone := make(chan error, 1)
	go func() { tickDone <- p.c.tickOnce(ctx) }()

	// Force all n drives to overlap before any completes, so ObserveDrive is
	// genuinely invoked concurrently (each fires as its driveOne returns).
	testwait.External(t, "all-drives-concurrent",
		func() bool { return p.peak.Load() >= int64(n) },
		testtime.D2s, testtime.D1ms)

	p.releaseAll()
	if err := testwait.Deterministic(t, tickDone, "tickOnce-returned"); err != nil {
		t.Errorf("tickOnce returned error: %v", err)
	}
	if got := rec.count(); got != n {
		t.Errorf("ObserveDrive call count = %d, want %d (one per concurrent drive)", got, n)
	}
	for i, r := range rec.drives {
		if r != executor.DriveOK {
			t.Errorf("drive[%d] result = %q, want %q", i, r, executor.DriveOK)
		}
	}
}
