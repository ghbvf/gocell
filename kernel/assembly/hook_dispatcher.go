package assembly

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// Default queue / timeout settings for hookDispatcher. Tuned for GoCell's
// event volume: a single assembly emits on the order of (cells * 4 phases *
// 2 start/stop) ≤ 100 events over its lifetime, so a 128-slot queue leaves
// margin for bursts during shutdown without growing the memory footprint.
const (
	DefaultHookObserverQueueSize    = 128
	DefaultHookObserverSinkTimeout  = 5 * time.Second
	DefaultHookObserverDrainTimeout = 5 * time.Second
)

const maxHookObserverPanicLogBytes = 256

// Drop reason label values emitted on hook_observer_dropped_total (bare name;
// the Provider's Namespace field adds the "gocell_" prefix to produce the
// final Prometheus fqName "gocell_hook_observer_dropped_total").
// Exported so downstream tests (and operators wiring Grafana variables)
// can refer to the reason string by symbolic name instead of duplicating
// the literal — a rename of the literal flips every call site at once.
const (
	DropReasonQueueFull     = "queue_full"
	DropReasonSinkTimeout   = "sink_timeout"
	DropReasonObserverPanic = "observer_panic"
)

// hookDispatcher is a single-worker asynchronous fan-out for HookEvents so
// that slow or misbehaving observers cannot stall assembly Start/Stop.
//
// Design:
//   - **Non-blocking emit** via `select { case ch <- e: default: drop }`.
//     Assembly critical path never blocks on a stuck sink. Full queue is
//     reported via `hook_observer_dropped_total{reason="queue_full"}`.
//   - **Per-sink timeout** via goroutine+channel race (mirroring
//     go.uber.org/fx app.go withTimeout); a slow sink is abandoned to its
//     own tracked goroutine and counted `reason="sink_timeout"`. Stop()
//     waits for timed-out sinks within the existing drain budget so normal
//     shutdowns do not leave observer goroutines behind, while permanently
//     stuck observers still cannot block assembly shutdown forever.
//   - **Eager lifecycle**: `newHookDispatcher` starts the worker
//     immediately; `stop(timeout)` closes the channel, waits for drain up
//     to timeout, then returns. `sync.Once` protects idempotent stop.
//
// ref: k8s.io/client-go tools/record/event.go@master — broadcaster +
// buffered channel + drop-if-full semantics for event recorders.
// ref: go.uber.org/fx app.go@master — withTimeout goroutine pattern for
// bounded callback execution.
// hookItem wraps a HookEvent with an optional sync fence so the worker
// can process events and explicit synchronization markers through the
// same FIFO channel (ordered delivery guaranteed by Go channel semantics).
type hookItem struct {
	evt  *cell.HookEvent // nil for sync fences
	sync chan struct{}   // non-nil for sync fences; worker closes after
	// advancing past this item
}

type hookDispatcher struct {
	ch          chan hookItem
	observer    cell.LifecycleHookObserver
	sinkTimeout time.Duration
	dropped     metrics.CounterVec // labeled by reason
	clock       clock.Clock
	wg          sync.WaitGroup
	sinkWg      sync.WaitGroup
	sinkMu      sync.Mutex
	sinkActive  int
	sinkIdle    chan struct{}
	stopOnce    sync.Once
	queueWarn   sync.Once
	done        chan struct{} // closed when the worker loop exits
}

// dispatcherConfig bundles the knobs of newHookDispatcher so the
// constructor has one parameter instead of four. The struct also makes
// new knobs (drain policy, reason-label customisation …) backward
// compatible — adding a field can't silently reorder arguments at call
// sites.
//
// ref: Go std-lib convention (net/http.Server, sql.TxOptions) — options
// structs are preferred over positional parameters once the knob count
// reaches 3-4.
type dispatcherConfig struct {
	// Observer receives HookEvents. Required (assembly substitutes
	// NopHookObserver when caller passed nil).
	Observer cell.LifecycleHookObserver
	// QueueSize bounds the pending event buffer. Zero / negative uses
	// DefaultHookObserverQueueSize.
	QueueSize int
	// SinkTimeout bounds one OnHookEvent call. Zero / negative uses
	// DefaultHookObserverSinkTimeout.
	SinkTimeout time.Duration
	// Provider backs drop/queue-depth metrics. Nil falls back to
	// metrics.NopProvider — dispatcher still works, emissions go nowhere.
	Provider metrics.Provider
	// Clock is the time source for flush deadline timers. Required; use
	// clock.Real() in production and clockmock.New() in tests.
	Clock clock.Clock
}

// newHookDispatcher constructs + eagerly starts a dispatcher.
func newHookDispatcher(cfg dispatcherConfig) *hookDispatcher {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultHookObserverQueueSize
	}
	if cfg.SinkTimeout <= 0 {
		cfg.SinkTimeout = DefaultHookObserverSinkTimeout
	}
	if cfg.Provider == nil {
		cfg.Provider = metrics.NopProvider{}
	}
	clock.MustHaveClock(cfg.Clock, "assembly.newHookDispatcher")

	dropped, err := cfg.Provider.CounterVec(metrics.CounterOpts{
		Name:       "hook_observer_dropped_total",
		Help:       "Total number of hook events dropped by the async hook dispatcher, partitioned by reason.",
		LabelNames: []string{"reason"},
	})
	if err != nil {
		// Not fatal: fall back to NopProvider so emission path stays safe.
		// Registration failures usually mean a duplicate name — this package
		// should be used once per assembly per process; duplicate is caller
		// misuse and should not break assembly lifecycle.
		slog.Warn("assembly: hook dispatcher metric registration failed; falling back to Nop",
			slog.Any("error", err))
		dropped, _ = metrics.NopProvider{}.CounterVec(metrics.CounterOpts{LabelNames: []string{"reason"}})
	}

	d := &hookDispatcher{
		ch:          make(chan hookItem, cfg.QueueSize),
		observer:    cfg.Observer,
		sinkTimeout: cfg.SinkTimeout,
		dropped:     dropped,
		clock:       cfg.Clock,
		sinkIdle:    closedChannel(),
		done:        make(chan struct{}),
	}
	d.wg.Add(1)
	go d.run()
	return d
}

// emit sends e to the worker. When the buffer is full or the dispatcher is
// already stopped, the event is dropped and counted.
//
// The stopped-select case accepts the event into the closed channel path
// intentionally: after stop() closes d.ch, any subsequent emit is a caller
// bug (emit-after-stop), but we treat it as queue_full rather than panic.
//
// The emit path uses a defer+recover guard so that sending after stop()
// (which closes d.ch) is surfaced as a dropped event rather than a panic.
// The common fast path (channel open, room available) does not enter
// defer overhead because runtime channel sends never panic in that case.
func (d *hookDispatcher) emit(e cell.HookEvent) {
	defer func() {
		if r := recover(); r != nil {
			// Send on closed channel after stop(). Count as queue_full
			// because from the emitter's perspective the queue is
			// unavailable.
			d.dropQueueFull(e)
		}
	}()
	evt := e
	select {
	case d.ch <- hookItem{evt: &evt}:
	default:
		d.dropQueueFull(e)
	}
}

// flush enqueues a synchronization fence and waits for the worker to
// reach it. Returns true if all events previously emitted have been
// delivered; false if the fence itself could not be enqueued within
// timeout or the worker did not reach it in time. Never blocks the
// dispatcher — a timed-out flush abandons the fence like a normal event.
//
// Callers (primarily tests) use flush to observe a deterministic state
// for assertions that need all in-flight events processed before reading
// observer state.
func (d *hookDispatcher) flush(timeout time.Duration) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			// Same recovery as emit: sending on a closed channel means the
			// dispatcher has already stopped accepting events. A post-stop
			// flush is therefore a no-op success for callers that only need
			// a stable "not accepting fences" state; it does not prove a
			// timed-out stop drained observer sinks.
			ok = true
		}
	}()
	if timeout <= 0 {
		timeout = time.Second
	}
	signal := make(chan struct{})
	item := hookItem{sync: signal}

	// Use a single Timer for both enqueue and wait-for-fence legs so a
	// flush against a slow worker still respects the caller's budget.
	// Semantics subtlety: whichever leg first selects on Timer.C consumes
	// the event; if enqueue uses most of the budget, the wait-for-fence
	// leg may see an already-drained channel and immediately return
	// false. Tests pass timeouts >= 500ms (see hook_dispatcher_test.go)
	// to stay comfortably above this pathology. Callers that need
	// independent budgets can call flush twice.
	t := d.clock.NewTimerAt(d.clock.Now().Add(timeout))
	defer t.Stop()

	select {
	case d.ch <- item:
	case <-t.C():
		return false
	}
	select {
	case <-signal:
		return true
	case <-t.C():
		return false
	}
}

// run is the worker loop. It exits when ch is closed (by stop) AND the
// buffer is drained.
func (d *hookDispatcher) run() {
	defer d.wg.Done()
	defer close(d.done)
	for item := range d.ch {
		if item.sync != nil {
			close(item.sync)
			continue
		}
		if item.evt != nil {
			d.dispatchOne(*item.evt)
		}
	}
}

// dispatchOne calls the observer for one event with a per-sink timeout and
// panic isolation. A slow sink is abandoned to its goroutine; the worker
// advances to the next event so a stuck observer cannot halt delivery.
func (d *hookDispatcher) dispatchOne(e cell.HookEvent) {
	result := make(chan struct{}, 1)
	d.beginSink()
	go func() {
		defer func() {
			defer d.finishSink()
			if r := recover(); r != nil {
				d.dropped.With(metrics.Labels{"reason": DropReasonObserverPanic}).Inc()
				slog.Error("lifecycle: hook observer panicked",
					slog.String("cell", e.CellID),
					slog.String("hook", string(e.Hook)),
					slog.String("panic_type", hookObserverPanicType(r)),
					slog.String("panic", sanitizeHookObserverPanicValue(r)))
			}
			select {
			case result <- struct{}{}:
			default:
			}
		}()
		d.observer.OnHookEvent(e)
	}()

	t := d.clock.NewTimerAt(d.clock.Now().Add(d.sinkTimeout))
	select {
	case <-result:
		// Normal completion or caught panic.
		t.Stop()
	case <-t.C():
		t.Stop()
		d.dropped.With(metrics.Labels{"reason": DropReasonSinkTimeout}).Inc()
		slog.Warn("lifecycle: hook observer exceeded sink timeout; abandoning",
			slog.String("cell", e.CellID),
			slog.String("hook", string(e.Hook)),
			slog.Duration("timeout", d.sinkTimeout))
	}
}

// stop closes the channel, then waits for the worker to drain remaining
// events, drainTimeout to elapse, or ctx cancellation — whichever comes first.
// After stop returns, the dispatcher is no longer usable; emit() will see a
// full (actually closed) channel via the default branch and count the event as
// queue_full.
//
// stop is safe to call multiple times; only the first invocation closes the
// channel.
func (d *hookDispatcher) stop(ctx context.Context, drainTimeout time.Duration) {
	d.stopOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		close(d.ch)
		if drainTimeout <= 0 {
			drainTimeout = DefaultHookObserverDrainTimeout
		}
		t := d.clock.NewTimerAt(d.clock.Now().Add(drainTimeout))
		defer t.Stop()
		select {
		case <-d.done:
			// Worker drained cleanly; no future sinkWg.Add calls can occur.
		case <-t.C():
			slog.Warn("assembly: hook dispatcher drain timed out; abandoning worker",
				slog.Duration("timeout", drainTimeout))
			return
		case <-ctx.Done():
			slog.Warn("assembly: hook dispatcher drain canceled; abandoning worker",
				slog.Any("error", ctx.Err()))
			return
		}

		select {
		case <-d.currentSinkIdle():
			d.sinkWg.Wait()
		case <-t.C():
			slog.Warn("assembly: hook dispatcher sink drain timed out; abandoning observer sinks",
				slog.Duration("timeout", drainTimeout))
		case <-ctx.Done():
			slog.Warn("assembly: hook dispatcher sink drain canceled; abandoning observer sinks",
				slog.Any("error", ctx.Err()))
		}
	})
}

func (d *hookDispatcher) dropQueueFull(e cell.HookEvent) {
	d.dropped.With(metrics.Labels{"reason": DropReasonQueueFull}).Inc()
	d.queueWarn.Do(func() {
		slog.Warn("assembly: hook dispatcher queue full; dropping hook event",
			slog.String("reason", DropReasonQueueFull),
			slog.String("cell", e.CellID),
			slog.String("hook", string(e.Hook)))
	})
}

func (d *hookDispatcher) beginSink() {
	d.sinkWg.Add(1)
	d.sinkMu.Lock()
	if d.sinkActive == 0 {
		d.sinkIdle = make(chan struct{})
	}
	d.sinkActive++
	d.sinkMu.Unlock()
}

func (d *hookDispatcher) finishSink() {
	d.sinkMu.Lock()
	d.sinkActive--
	if d.sinkActive == 0 {
		close(d.sinkIdle)
	}
	d.sinkMu.Unlock()
	d.sinkWg.Done()
}

func (d *hookDispatcher) currentSinkIdle() <-chan struct{} {
	d.sinkMu.Lock()
	defer d.sinkMu.Unlock()
	return d.sinkIdle
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func sanitizeHookObserverPanicValue(r any) string {
	return truncateUTF8Bytes(redaction.RedactString(fmt.Sprintf("%v", r)), maxHookObserverPanicLogBytes)
}

func hookObserverPanicType(r any) string {
	return fmt.Sprintf("%T", r)
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	truncated := s[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}
