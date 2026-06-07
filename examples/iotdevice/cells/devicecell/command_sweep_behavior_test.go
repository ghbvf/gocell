package devicecell

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/eventbus"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// command_sweep_behavior_test.go covers the END-TO-END behavior of the migrated
// device-command sweeper (kernel Sweeper → reconcile.Reconciler → reconcile.Loop
// on a TickerTrigger), which the prior tests left unpinned (they only asserted
// the lifecycle hook starts/stops cleanly). Two properties are pinned:
//
//   - Expiry: a fake-clock pulse drives the full
//     TickerTrigger → Loop → Reconcile → SweepTick → Queue.Ack(AckTimeout) chain,
//     terminating an overdue command (StatusExpired).
//   - Single source: with WithoutDefaultRequeue the TickerTrigger is the SOLE
//     periodic source — each pulse produces exactly one sweep
//     (reconcile_total{result="success"} increments by one per tick, never two).
//     This is the faithful, deterministic regression guard for the double-sweep
//     finding: because there is no default-tick self-requeue, the sweep count is
//     driven only by the fake clock, with no real-clock requeue muddying it.
//
// The cell's business clock (c.clk = fake) drives BOTH the TickerTrigger cadence
// and the Sweeper's expiry "now". The Loop's control-plane clock (startup probe,
// requeue timers) is the sealed real-only clock, so Start settles in real time
// regardless of the fake business clock — that is why OnStart returns promptly.

// sweepTestBase is a fixed wall time for the fake business clock so deadlines are
// deterministic (no Date.Now()).
var sweepTestBase = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

// sweepWaitTimeout / sweepWaitTick bound the real-time poll for the async sweep
// effect (the ticker pulse is delivered on a goroutine; the work is observed via
// testwait, not a fixed sleep).
const (
	sweepWaitTimeout = 2 * time.Second
	sweepWaitTick    = 5 * time.Millisecond
)

// newSweepTestCell builds a DeviceCell on the supplied fake clock + queue, with
// an optional metrics provider. The publisher/eventbus use a real clock (they
// play no part in sweep timing); only the cell clock (sweep + ticker cadence) is
// the fake one.
func newSweepTestCell(t *testing.T, fc clock.Clock, q kcommand.Queue, mp metrics.Provider) *DeviceCell {
	t.Helper()
	opts := []Option{
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),
		WithBootstrapEmitter(testBootstrapEmitter()),
		WithCommandRegistry(commandruntime.NewRegistry()),
	}
	if mp != nil {
		opts = append(opts, WithMetricsProvider(mp))
	}
	c := NewDeviceCell(fc, opts...)
	c.RegisterCommandQueue(q)
	return c
}

// startSweeperHook Inits the cell and starts the sweeper lifecycle hook, returning
// the hook so the caller can OnStop it. OnStart is non-blocking (Loop.Start spawns
// the pool + a real-clock startup probe and returns).
func startSweeperHook(t *testing.T, c *DeviceCell, ctx context.Context) func(context.Context) error {
	t.Helper()
	rec := newTestRec()
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	require.Len(t, snap.LifecycleHooks, 1, "expect one lifecycle hook (sweeper)")
	hook := snap.LifecycleHooks[0]
	require.NoError(t, hook.OnStart(ctx), "OnStart must start the loop without error")
	return hook.OnStop
}

// TestDeviceCell_CommandSweep_ExpiresOverdueCommand pins the end-to-end expiry
// chain (F2): a single ticker pulse drives the migrated sweeper to terminate an
// overdue command via Queue.Ack(AckTimeout) → StatusExpired.
func TestDeviceCell_CommandSweep_ExpiresOverdueCommand(t *testing.T) {
	defer goleak.VerifyNone(t)

	fc := clockmock.New(sweepTestBase)
	q := commandtest.NewInMemQueue()
	q.Now = fc.Now // keep Ack timestamps on the same (fake) timeline

	const expiredID = "cmd-overdue"
	require.NoError(t, q.WriteCommand(context.Background(), kcommand.Entry{
		ID:          expiredID,
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Status:      kcommand.StatusPending,
		// OverallDeadline = 1h, created 2h before "now" → already overdue.
		CreatedAt: sweepTestBase.Add(-2 * time.Hour),
		Timeouts:  kcommand.Timeouts{OverallDeadline: time.Hour},
	}))

	c := newSweepTestCell(t, fc, q, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	onStop := startSweeperHook(t, c, ctx)

	// One ticker pulse → one full sweep at fc.Now() → overdue command terminated.
	fc.Advance(commandSweepInterval)

	testwait.External(t, "command-swept-to-expired",
		func() bool {
			got, err := q.GetCommand(context.Background(), expiredID)
			return err == nil && got != nil && got.Status == kcommand.StatusExpired
		},
		sweepWaitTimeout, sweepWaitTick,
		"overdue command must be swept to StatusExpired after one ticker pulse")

	require.NoError(t, onStop(context.Background()), "OnStop must drain cleanly")
}

// TestDeviceCell_CommandSweep_TickerIsSoleSource pins the single-source invariant
// (F1) and the metric emission (F3): with WithoutDefaultRequeue the TickerTrigger
// is the only periodic driver, so each pulse records exactly one
// reconcile_total{reconciler="devicecommand_sweeper",result="success"} — two
// pulses ⇒ exactly two successes, never four (which a second periodic source —
// the default-tick self-requeue — would produce).
func TestDeviceCell_CommandSweep_TickerIsSoleSource(t *testing.T) {
	defer goleak.VerifyNone(t)

	fc := clockmock.New(sweepTestBase)
	q := commandtest.NewInMemQueue()
	q.Now = fc.Now
	rp := newRecordingProvider()

	c := newSweepTestCell(t, fc, q, rp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	onStop := startSweeperHook(t, c, ctx)

	successLabels := metrics.Labels{"reconciler": "devicecommand_sweeper", "result": "success"}
	successCount := func() int64 { return rp.counterValue(metricReconcileTotal, successLabels) }

	// Pulse 1 → exactly one success sweep (empty scan is still a success).
	fc.Advance(commandSweepInterval)
	testwait.External(t, "first-pulse-one-success",
		func() bool { return successCount() >= 1 },
		sweepWaitTimeout, sweepWaitTick,
		"first ticker pulse must record one success sweep")

	// Pulse 2 → exactly one more. No default-tick self-requeue exists, so the
	// only way to reach 2 is two ticker pulses (and the only way to overshoot to
	// >2 would be a second, redundant periodic source — the bug under test).
	fc.Advance(commandSweepInterval)
	testwait.External(t, "second-pulse-two-successes",
		func() bool { return successCount() >= 2 },
		sweepWaitTimeout, sweepWaitTick,
		"second ticker pulse must record a second success sweep")

	assert.Equal(t, int64(2), successCount(),
		"exactly one sweep per ticker pulse: WithoutDefaultRequeue makes the ticker the sole periodic source")

	require.NoError(t, onStop(context.Background()), "OnStop must drain cleanly")
}

// metricReconcileTotal is the reconcile counter family name (kept local to avoid
// importing kernel/reconcile internals).
const metricReconcileTotal = "reconcile_total"

// recordingProvider is a minimal metrics.Provider spy that tallies counter
// increments by (name, label-set). Histogram / gauge instruments the Loop also
// registers (duration, in-flight, leader) are accepted but ignored — only
// reconcile_total is asserted here.
type recordingProvider struct {
	mu       sync.Mutex
	counters map[string]int64
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{counters: map[string]int64{}}
}

func (p *recordingProvider) CounterVec(o metrics.CounterOpts) (metrics.CounterVec, error) {
	return &recCounterVec{p: p, name: o.Name}, nil
}

func (p *recordingProvider) HistogramVec(metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return recNopHistVec{}, nil
}

func (p *recordingProvider) GaugeVec(metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return recNopGaugeVec{}, nil
}
func (p *recordingProvider) Unregister(metrics.Collector) error { return nil }

func (p *recordingProvider) counterValue(name string, l metrics.Labels) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counters[recKey(name, l)]
}

func recKey(name string, l metrics.Labels) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
	}
	return b.String()
}

type recCounterVec struct {
	p    *recordingProvider
	name string
}

func (v *recCounterVec) Registered() bool { return true }
func (v *recCounterVec) With(l metrics.Labels) metrics.Counter {
	return &recCounter{p: v.p, key: recKey(v.name, l)}
}

type recCounter struct {
	p   *recordingProvider
	key string
}

func (c *recCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c *recCounter) Add(_ context.Context, delta float64) {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	c.p.counters[c.key] += int64(delta)
}

// no-op histogram / gauge instruments — the Loop registers a duration histogram
// and in-flight / leader gauges that this spy does not assert on.
type recNopHistVec struct{}

func (recNopHistVec) Registered() bool                      { return true }
func (recNopHistVec) With(metrics.Labels) metrics.Histogram { return recNopHist{} }

type recNopHist struct{}

func (recNopHist) Observe(context.Context, float64) {}

type recNopGaugeVec struct{}

func (recNopGaugeVec) Registered() bool                  { return true }
func (recNopGaugeVec) With(metrics.Labels) metrics.Gauge { return recNopGauge{} }

type recNopGauge struct{}

func (recNopGauge) Set(context.Context, float64) {}
func (recNopGauge) Inc(context.Context)          {}
func (recNopGauge) Dec(context.Context)          {}
func (recNopGauge) Add(context.Context, float64) {}
