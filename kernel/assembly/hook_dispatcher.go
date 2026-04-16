package assembly

import (
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
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

// Drop reasons emitted as label values on gocell_hook_observer_dropped_total.
// Exported for operators and reviewers grepping dashboard configs.
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
//     reported via `gocell_hook_observer_dropped_total{reason="queue_full"}`.
//   - **Per-sink timeout** via goroutine+channel race (mirroring
//     go.uber.org/fx app.go withTimeout); a slow sink is abandoned to its
//     own goroutine and counted `reason="sink_timeout"`. A broken observer
//     that blocks forever leaks one goroutine per emission; that is the
//     observer's bug, not ours, and is materially safer than blocking the
//     assembly.
//   - **Eager lifecycle**: `newHookDispatcher` starts the worker
//     immediately; `stop(timeout)` closes the channel, waits for drain up
//     to timeout, then returns. `sync.Once` protects idempotent stop.
//
// ref: k8s.io/client-go tools/record/event.go@master — broadcaster +
// buffered channel + drop-if-full semantics for event recorders.
// ref: go.uber.org/fx app.go@master — withTimeout goroutine pattern for
// bounded callback execution.
// hookItem wraps a HookEvent with an optional sync fence so the worker
// can process events and explicit synchronisation markers through the
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
	wg          sync.WaitGroup
	stopOnce    sync.Once
	done        chan struct{} // closed when the worker loop exits
}

// newHookDispatcher constructs + eagerly starts a dispatcher. observer must
// be non-nil (assembly substitutes cell.NopHookObserver for caller nil).
// provider may be nil; it falls back to metrics.NopProvider.
func newHookDispatcher(observer cell.LifecycleHookObserver, queueSize int, sinkTimeout time.Duration, provider metrics.Provider) (*hookDispatcher, error) {
	if queueSize <= 0 {
		queueSize = DefaultHookObserverQueueSize
	}
	if sinkTimeout <= 0 {
		sinkTimeout = DefaultHookObserverSinkTimeout
	}
	if provider == nil {
		provider = metrics.NopProvider{}
	}

	dropped, err := provider.CounterVec(metrics.CounterOpts{
		Name:       "gocell_hook_observer_dropped_total",
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
		ch:          make(chan hookItem, queueSize),
		observer:    observer,
		sinkTimeout: sinkTimeout,
		dropped:     dropped,
		done:        make(chan struct{}),
	}
	d.wg.Add(1)
	go d.run()
	return d, nil
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
			d.dropped.With(metrics.Labels{"reason": DropReasonQueueFull}).Inc()
		}
	}()
	evt := e
	select {
	case d.ch <- hookItem{evt: &evt}:
	default:
		d.dropped.With(metrics.Labels{"reason": DropReasonQueueFull}).Inc()
	}
}

// flush enqueues a synchronisation fence and waits for the worker to
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
			// Same recovery as emit: sending on closed channel means the
			// dispatcher has already stopped, which implicitly means the
			// buffer was drained. Treat flush as successful.
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
	t := time.NewTimer(timeout)
	defer t.Stop()

	select {
	case d.ch <- item:
	case <-t.C:
		return false
	}
	select {
	case <-signal:
		return true
	case <-t.C:
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
	go func() {
		defer func() {
			if r := recover(); r != nil {
				d.dropped.With(metrics.Labels{"reason": DropReasonObserverPanic}).Inc()
				slog.Error("lifecycle: hook observer panicked",
					slog.String("cell", e.CellID),
					slog.String("hook", string(e.Hook)),
					slog.Any("panic", r))
			}
			select {
			case result <- struct{}{}:
			default:
			}
		}()
		d.observer.OnHookEvent(e)
	}()

	select {
	case <-result:
		// Normal completion or caught panic.
	case <-time.After(d.sinkTimeout):
		d.dropped.With(metrics.Labels{"reason": DropReasonSinkTimeout}).Inc()
		slog.Warn("lifecycle: hook observer exceeded sink timeout; abandoning",
			slog.String("cell", e.CellID),
			slog.String("hook", string(e.Hook)),
			slog.Duration("timeout", d.sinkTimeout))
	}
}

// stop closes the channel, then waits for the worker to drain remaining
// events or for drainTimeout to elapse — whichever comes first. After stop
// returns, the dispatcher is no longer usable; emit() will see a full
// (actually closed) channel via the default branch and count the event as
// queue_full.
//
// stop is safe to call multiple times; only the first invocation closes the
// channel.
func (d *hookDispatcher) stop(drainTimeout time.Duration) {
	d.stopOnce.Do(func() {
		close(d.ch)
		if drainTimeout <= 0 {
			drainTimeout = DefaultHookObserverDrainTimeout
		}
		select {
		case <-d.done:
			// Drained cleanly.
		case <-time.After(drainTimeout):
			slog.Warn("assembly: hook dispatcher drain timed out; abandoning worker",
				slog.Duration("timeout", drainTimeout))
		}
	})
}
