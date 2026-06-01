package distlock

import (
	"container/heap"
	"context"
	"log/slog"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
)

// lockID is a monotonically incrementing identifier for active lock entries.
type lockID = uint64

// lockState holds the runtime state for a single active lock.
//
// All fields except lock are read-only after construction; lock is the
// public-facing *Lock handle and the manager invokes lock.markCause(...)
// to signal lock-end events (release, lost).
type lockState struct {
	id    lockID
	key   string
	token string
	ttl   time.Duration
	lock  *Lock
}

// heapItem is an element of the renewal min-heap ordered by nextRenew time.
type heapItem struct {
	nextRenew time.Time
	id        lockID
	index     int // maintained by heap.Interface
}

// renewHeap is a min-heap of heapItems ordered by nextRenew (earliest first).
type renewHeap []*heapItem

func (h renewHeap) Len() int           { return len(h) }
func (h renewHeap) Less(i, j int) bool { return h[i].nextRenew.Before(h[j].nextRenew) }
func (h renewHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *renewHeap) Push(x any) {
	item := x.(*heapItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *renewHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[:n-1]
	return item
}

// Peek returns the earliest-deadline item without removing it.
// Caller must ensure h.Len() > 0.
func (h renewHeap) Peek() *heapItem { return h[0] }

// eventKind distinguishes add, remove, orphan, and renew-result events sent to
// the manager.
type eventKind int

const (
	eventAdd         eventKind = iota
	eventRemove                // initiated by release(): stops renewal AND calls Driver.Release
	eventOrphan                // initiated by orphan(): stops renewal WITHOUT any Driver I/O
	eventRenewResult           // posted back by the async Renew goroutine when the RPC returns
)

// managerEvent carries a single instruction to the manager goroutine.
//
// Field ownership by event kind:
//   - eventAdd:         state is set; id/item/held/renewErr/resultCh are zero.
//   - eventRemove:      id and resultCh are set; state/item/held/renewErr are zero.
//   - eventOrphan:      id and resultCh are set; state/item/held/renewErr are zero.
//   - eventRenewResult: id, item, held, renewErr are set; state and resultCh are nil.
type managerEvent struct {
	kind  eventKind
	state *lockState // eventAdd: the new lock to register
	id    lockID     // eventRemove / eventOrphan / eventRenewResult: lock identifier
	// resultCh receives the result on eventRemove/eventOrphan. Buffered
	// cap=1; the manager writes exactly once and the caller reads exactly once;
	// the channel is never closed.
	resultCh chan error
	// Fields used by eventRenewResult only.
	item     *heapItem // heap item popped at the start of the renew cycle
	held     bool      // true if the Renew RPC confirmed we still hold the key
	renewErr error     // non-nil if the Renew RPC (all attempts) returned an error
}

// ManagerSnapshot is a read-only view of the manager's current state.
// Exported for testing only.
type ManagerSnapshot struct {
	// Locks is the number of active locks currently tracked.
	Locks int
}

// Manager runs a single shared goroutine that owns the renewal heap and calls
// Driver.Renew for all active locks.
//
// The manager goroutine is the SOLE writer of the heap and locks map, which
// eliminates data races. External callers communicate exclusively through
// the events channel.
//
// Lifecycle:
//   - lazy-started on first Acquire (via lockerImpl.add)
//   - manager exits when the last lock is removed
//   - started is closed once the manager enters its main select loop
//   - drained is closed once the manager exits after the last lock removal
//
// ref: golang.org/x/tools/internal/event — single goroutine dispatch pattern
type Manager struct {
	driver Driver
	cfg    config

	// mu protects running, started, drained, managerDone, and snapshotLocks.
	// The heap/locks/items are owned exclusively by the run() goroutine.
	mu            sync.Mutex
	running       bool
	started       chan struct{}
	drained       chan struct{}
	managerDone   chan struct{} // closed by run() on exit; per-lifecycle instance
	snapshotLocks int           // protected by mu; written by manager-goroutine handlers, read by Snapshot()

	nextID atomic.Uint64
	// pendingDispositions counts how many locks have been added but have not yet
	// reached a terminal disposition. Each add() increments it; it is decremented
	// by either an eventRemove (release) or an eventOrphan — both terminal — so
	// the name covers both paths, not release alone. The manager drains only when
	// this reaches zero. Protected by mu (written by add/run; read by run).
	pendingDispositions int
	events              chan managerEvent

	// renewNotify receives a signal after each successful Driver.Renew call.
	// Buffered (cap 16) to avoid blocking the manager on slow consumers.
	// Only used in tests (via locktest helper and RenewNotify() accessor).
	// Never nil — allocated in newManager.
	renewNotify chan struct{}
}

func newManager(driver Driver, cfg config) *Manager {
	m := &Manager{
		driver:      driver,
		cfg:         cfg,
		events:      make(chan managerEvent, 64),
		started:     make(chan struct{}),
		drained:     make(chan struct{}),
		managerDone: make(chan struct{}),
		renewNotify: make(chan struct{}, 16),
	}
	return m
}

// Started returns a channel that is closed once the manager goroutine has
// entered its main select loop.
func (m *Manager) Started() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// Drained returns a channel that is closed once the manager goroutine exits
// after the last lock has been dispatched through eventRemove. Background
// Driver.Release I/O goroutines spawned by handleRemove may still be in
// flight when Drained closes — Release blocks the *caller* on the I/O result
// via the eventRemove resultCh, but the manager does not wait for those
// goroutines before exiting. Drained therefore signals "no more renewal
// activity will occur" rather than "all backend keys have been released".
func (m *Manager) Drained() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drained
}

// Snapshot returns a read-only view of current manager state.
func (m *Manager) Snapshot() ManagerSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ManagerSnapshot{Locks: m.snapshotLocks}
}

// RenewNotify returns a read-only channel that receives a signal after each
// successful Driver.Renew call. Intended for test synchronization only.
func (m *Manager) RenewNotify() <-chan struct{} {
	return m.renewNotify
}

// add sends a new lock to the manager goroutine and lazily starts it.
func (m *Manager) add(state *lockState) {
	m.mu.Lock()
	m.pendingDispositions++
	if !m.running {
		m.running = true
		// Fresh channels for this manager lifecycle.
		m.started = make(chan struct{})
		m.drained = make(chan struct{})
		m.managerDone = make(chan struct{})
		go m.run()
	}
	m.mu.Unlock()

	m.events <- managerEvent{kind: eventAdd, state: state}
}

// remove asks the manager to release a lock and returns the result of
// Driver.Release. It blocks until the release I/O completes (bounded by
// WithReleaseTimeout, default 5s). The manager goroutine is not blocked during
// the I/O — it dispatches the release to a background goroutine and signals
// resultCh when done, so the manager loop remains live for other events.
//
// Returns nil on successful Driver.Release, or a wrapped error on I/O failure
// or timeout. Idempotent: the sync.Once in the Acquire closure ensures remove
// is called at most once per lock.
func (m *Manager) remove(id lockID) error {
	resultCh := make(chan error, 1)
	m.events <- managerEvent{kind: eventRemove, id: id, resultCh: resultCh}
	return <-resultCh
}

// orphan asks the manager to stop renewing a lock WITHOUT calling Driver.Release.
// The backend key expires naturally after ≤1×TTL. Blocks until the manager has
// detached the lock from its heap (fast — no I/O). Always returns nil.
// Idempotent: the sync.Once in the Acquire closure ensures orphan is called at
// most once per lock.
func (m *Manager) orphan(id lockID) {
	resultCh := make(chan error, 1)
	m.events <- managerEvent{kind: eventOrphan, id: id, resultCh: resultCh}
	<-resultCh // wait for the manager to detach the lock
}

// run is the manager's main goroutine. It must not be called directly.
// It is the sole writer of locks, h, and items.
func (m *Manager) run() {
	// Label this goroutine for pprof stack traces and heap profiles.
	// "distlock"="manager" appears in go tool pprof goroutine listings.
	pprof.SetGoroutineLabels(pprof.WithLabels(context.Background(),
		pprof.Labels("distlock", "manager")))

	locks := make(map[lockID]*lockState)
	items := make(map[lockID]*heapItem)
	inflightRenew := make(map[lockID]context.CancelFunc)
	var h renewHeap
	heap.Init(&h)

	// Capture the per-lifecycle managerDone channel once so in-flight Renew
	// goroutines can reference it without racing the next lifecycle's allocation
	// in add().
	m.mu.Lock()
	managerDone := m.managerDone
	m.mu.Unlock()

	defer close(managerDone)

	slog.Debug("distlock: manager started")
	close(m.started)

	for {
		timer, timerC := m.nextTimer(&h)
		done := m.runOnce(timer, timerC, locks, items, &h, inflightRenew, managerDone)
		if done {
			return
		}
	}
}

// nextTimer returns a Timer (and its channel) for the earliest heap item,
// or nil/nil if the heap is empty.
//
// Uses the absolute-deadline NewTimerAt API (rather than deriving a duration
// off clock.Now() and calling NewTimer(d)) so that an interleaving
// FakeClock.Advance between Now() and timer creation cannot re-baseline the
// timer to a later deadline. Production realClock behaves identically either
// way; the discipline is required by FakeClock for deterministic tests and
// was the root cause of the TC-3 flake.
func (m *Manager) nextTimer(h *renewHeap) (clock.Timer, <-chan time.Time) {
	if h.Len() == 0 {
		return nil, nil
	}
	t := m.cfg.clock.NewTimerAt(h.Peek().nextRenew)
	return t, t.C()
}

// runOnce executes a single iteration of the manager's select loop.
// Returns true when the manager should exit.
func (m *Manager) runOnce(
	timer clock.Timer,
	timerC <-chan time.Time,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
	managerDone chan struct{},
) bool {
	select {
	case <-timerC:
		m.handleRenew(locks, items, h, inflightRenew, managerDone)
	case ev := <-m.events:
		if timer != nil {
			// Stop returns; no drain needed because we never reuse the timer object —
			// a fresh one is created next iteration. Future refactors using Reset must
			// add a drain-on-false guard here.
			timer.Stop()
		}
		if m.dispatchEvent(ev, locks, items, h, inflightRenew) {
			return true
		}
	}
	return false
}

// dispatchEvent handles a single manager event. Returns true when the manager
// should drain and exit (last lock accounted for).
func (m *Manager) dispatchEvent(
	ev managerEvent,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
) bool {
	switch ev.kind {
	case eventAdd:
		m.handleAdd(ev.state, locks, items, h)
	case eventRemove:
		m.handleRemove(ev, locks, items, h, inflightRenew)
		// Both eventRemove and eventOrphan account for one pending-release slot
		// (each add() increments pendingDispositions once).
		return m.decPendingAndMaybeDrain()
	case eventOrphan:
		m.handleOrphan(ev, locks, items, h, inflightRenew)
		return m.decPendingAndMaybeDrain()
	case eventRenewResult:
		m.handleRenewResult(ev, locks, items, h, inflightRenew)
		// eventRenewResult is NOT a terminal disposition — it does not account
		// for a pendingDispositions slot.
	}
	return false
}

// decPendingAndMaybeDrain decrements pendingDispositions. If it reaches zero it
// closes the drained channel, marks the manager stopped, and returns true
// (signals runOnce to exit). The manager exits when all pending-release slots
// have been accounted for (via either eventRemove or eventOrphan), regardless
// of whether Driver.Release I/O goroutines from eventRemove are still in-flight.
func (m *Manager) decPendingAndMaybeDrain() bool {
	m.mu.Lock()
	m.pendingDispositions--
	pending := m.pendingDispositions
	m.mu.Unlock()
	if pending == 0 {
		m.mu.Lock()
		m.running = false
		m.snapshotLocks = 0
		drained := m.drained
		m.mu.Unlock()
		slog.Debug("distlock: manager drained")
		close(drained)
		return true
	}
	return false
}

// handleAdd registers a new lock in the heap.
func (m *Manager) handleAdd(state *lockState, locks map[lockID]*lockState, items map[lockID]*heapItem, h *renewHeap) {
	locks[state.id] = state
	item := &heapItem{
		nextRenew: m.cfg.clock.Now().Add(time.Duration(float64(state.ttl) * m.cfg.renewFraction)),
		id:        state.id,
	}
	items[state.id] = item
	heap.Push(h, item)

	m.mu.Lock()
	m.snapshotLocks = len(locks)
	m.mu.Unlock()
}

// handleRenew pops the earliest item from the heap and spawns a goroutine to
// call Driver.Renew (with retry budget for transient I/O errors). The goroutine
// posts an eventRenewResult back to the manager event channel when complete.
//
// The manager loop never blocks on Driver.Renew I/O — identical to the
// handleRemove/Driver.Release pattern. This ensures that Lock.Orphan() /
// Coordinator.Stop() are never delayed by a slow or unreachable backend.
//
// Retry semantics (executed inside the goroutine):
//   - held=false (ownership lost): permanent — skip retries, post result immediately.
//   - err != nil (I/O error): transient by default — retry up to maxRenewAttempts.
//   - All attempts share the same renewTimeout budget derived from TTL and driftFactor.
//
// inflightRenew[id] is set to the cancel func of the Renew RPC context so
// detachLock (called by orphan/remove) can cancel an in-flight Renew and
// prevent it from extending a key that should have expired.
func (m *Manager) handleRenew(
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
	managerDone chan struct{},
) {
	if h.Len() == 0 {
		return
	}
	item := heap.Pop(h).(*heapItem)
	delete(items, item.id)

	state, ok := locks[item.id]
	if !ok {
		// Already removed (lost race between timer and remove event).
		slog.Debug("distlock: renew skipped; lock already removed", "lock_id", item.id)
		return
	}

	ttl := state.ttl
	drift := time.Duration(float64(ttl) * m.cfg.driftFactor)
	// Compute deadline for the Renew I/O call: clock.Now() + ttl*(1-driftFactor).
	// Using clock.Now() (not time.Now()) ensures the deadline is computed in the
	// same time domain as the FakeClock in tests, so FakeDriver.LastRenewDeadline
	// reports the correct value for TC-12 drift-factor validation.
	//
	// When using FakeClock (zero start time), the computed deadline is in the
	// real past, so context.WithDeadline creates an already-expired context.
	// This is acceptable: FakeDriver.Renew does not block on the context (it
	// runs synchronously), and the Renew goroutine posts its result regardless
	// of context state (no ctx.Err() guard). On the real backend the context
	// properly bounds the RPC duration.
	renewTimeout := ttl - drift
	var renewCtx context.Context
	var cancel context.CancelFunc
	if renewTimeout > 0 {
		deadline := m.cfg.clock.Now().Add(renewTimeout)
		renewCtx, cancel = context.WithDeadline(context.Background(), deadline)
	} else {
		renewCtx, cancel = context.WithCancel(context.Background())
	}

	// Record the cancel so detachLock can abort the in-flight Renew.
	inflightRenew[item.id] = cancel

	// Spawn the Renew I/O in a background goroutine so the manager loop stays
	// live for other events (orphan, remove, add). Mirror of handleRemove's
	// Driver.Release goroutine.
	//
	// The goroutine always attempts to post back an eventRenewResult.
	// handleRenewResult handles the "late result" case (lock already detached
	// by orphan/remove) safely: if inflightRenew[id] was deleted and the lock
	// is absent from locks[], the result is discarded with a Debug log.
	go m.renewWorker(renewCtx, cancel, item, state, managerDone)
}

// renewWorker executes Driver.Renew with retry logic in a background goroutine
// and posts the result back to the manager event channel. It is launched by
// handleRenew and runs fully outside the manager goroutine.
//
// Ownership-lost (held=false, err=nil) is treated as permanent and posted
// immediately without retrying. I/O errors are retried up to maxRenewAttempts.
func (m *Manager) renewWorker(
	renewCtx context.Context,
	cancel context.CancelFunc,
	item *heapItem,
	state *lockState,
	managerDone chan struct{},
) {
	defer cancel()
	held, lastErr := m.runRenewAttempts(renewCtx, item, state, managerDone)
	if held || lastErr != nil { // normal or error result to report
		select {
		case m.events <- managerEvent{kind: eventRenewResult, id: item.id, item: item, held: held, renewErr: lastErr}:
		case <-managerDone:
		}
	}
}

// runRenewAttempts calls Driver.Renew up to maxRenewAttempts times.
// Returns (held=true, nil) on success, (false, nil) on permanent ownership
// loss (posts the ownership-lost eventRenewResult itself), or (false, err) when
// all attempts return I/O errors.
//
// When ownership-lost is detected (held=false, err=nil), the event is posted
// directly from this function and (false, nil) is returned to renewWorker as a
// sentinel meaning "already posted, do not post again".
func (m *Manager) runRenewAttempts(
	renewCtx context.Context,
	item *heapItem,
	state *lockState,
	managerDone chan struct{},
) (held bool, lastErr error) {
	maxAttempts := m.cfg.maxRenewAttempts
	key := state.key
	ttl := state.ttl
	for attempt := range maxAttempts {
		wasHeld, err := m.driver.Renew(renewCtx, key, state.token, ttl)
		if err == nil && !wasHeld {
			// Permanent: ownership lost. Post immediately and return sentinel.
			slog.Error("distlock: renewal ownership lost", "key", key, "op", "Renew", "ttl", ttl, "attempts", 1)
			select {
			case m.events <- managerEvent{kind: eventRenewResult, id: item.id, item: item, held: false, renewErr: nil}:
			case <-managerDone:
			}
			return false, nil // sentinel: event already posted
		}
		if err == nil {
			return true, nil // success
		}
		lastErr = err
		slog.Debug("distlock: renewal I/O error (will retry)",
			"key", key, "op", "Renew",
			"attempt", attempt+1, "max_attempts", maxAttempts, "error", err)
	}
	return false, lastErr // all attempts exhausted
}

// handleRenewResult processes the result of an async Driver.Renew call.
// Called by dispatchEvent when eventRenewResult arrives.
//
// Three outcomes:
//  1. renewErr != nil: all Renew attempts failed → mark lock lost.
//  2. renewErr == nil && !held: ownership lost permanently → mark lock lost.
//  3. renewErr == nil && held: success → re-queue item and signal renewNotify.
func (m *Manager) handleRenewResult(
	ev managerEvent,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
) {
	// The call returned; remove the cancel func from the inflight map.
	delete(inflightRenew, ev.id)

	state, ok := locks[ev.id]
	if !ok {
		// Late result: lock was already detached (orphaned / removed / lost) while
		// the Renew goroutine was in flight. Safe to ignore.
		slog.Debug("distlock: renew result for detached lock", "lock_id", ev.id)
		return
	}

	if ev.renewErr != nil {
		// All attempts exhausted — declare lock lost.
		slog.Error("distlock: renewal I/O error; budget exhausted; lock lost",
			"key", state.key,
			"op", "Renew",
			"ttl", state.ttl,
			"attempts", m.cfg.maxRenewAttempts,
			"error", ev.renewErr)
		state.lock.markCause(ErrLockLost)
		delete(locks, ev.id)
		m.mu.Lock()
		m.snapshotLocks = len(locks)
		m.mu.Unlock()
		return
	}

	if !ev.held {
		// Ownership lost permanently (token mismatch or key gone).
		state.lock.markCause(ErrLockLost)
		delete(locks, ev.id)
		m.mu.Lock()
		m.snapshotLocks = len(locks)
		m.mu.Unlock()
		return
	}

	// Success — re-queue the item with the next renew time.
	ttl := state.ttl
	ev.item.nextRenew = m.cfg.clock.Now().Add(time.Duration(float64(ttl) * m.cfg.renewFraction))
	ev.item.index = -1
	items[ev.id] = ev.item
	heap.Push(h, ev.item)

	// Signal renewNotify so tests can synchronize on renew completion.
	select {
	case m.renewNotify <- struct{}{}:
	default:
	}
}

// detachLock removes the lock with the given id from the manager's heap and
// locks map, sets its cause, and updates snapshotLocks under mu. Returns the
// detached lockState (and ok=true) if the lock was found, or (nil, false) if it
// was already absent (lost via renewal failure before the terminal event arrived).
//
// If a Renew is in-flight for this lock, its context is canceled (best-effort)
// to keep the orphaned key's expiry close to one TTL window from the last
// successful renewal. The cancel is not atomic with the backend: a Renew whose
// write already landed extends the key one more cycle from its commit, so the
// bound is "~1×TTL from the last successful renewal", not a hard cap from the
// detach call (see Lock.Orphan godoc).
//
// Called by handleRemove and handleOrphan to share the heap-detach path.
//
// detachLock intentionally does not log. Callers (handleRemove and handleOrphan)
// own their observability signals because the two paths have different log levels
// and contexts: handleRemove logs a warning on I/O error; handleOrphan logs at
// Debug. Centralizing the log call here would require a caller-supplied level and
// message, adding indirection for minimal benefit.
func (m *Manager) detachLock(
	id lockID,
	cause error,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
) (*lockState, bool) {
	state, ok := locks[id]
	if !ok {
		return nil, false
	}
	delete(locks, id)
	if item, has := items[id]; has {
		heap.Remove(h, item.index)
		delete(items, id)
	}
	// Cancel any in-flight Renew (best-effort) so an orphaned/removed key's
	// expiry stays close to one TTL window from the last successful renewal. The
	// cancel is racy against a Renew that already wrote to the backend; that case
	// extends the key one more cycle from its commit (see Lock.Orphan godoc).
	if cancel, ok := inflightRenew[id]; ok {
		cancel()
		delete(inflightRenew, id)
	}
	m.mu.Lock()
	m.snapshotLocks = len(locks)
	m.mu.Unlock()
	state.lock.markCause(cause)
	return state, true
}

// handleRemove processes a remove event. Driver.Release I/O runs in a background
// goroutine so the manager loop is not blocked. The result is sent to ev.resultCh
// so the remove() caller can observe the outcome. ev.resultCh is always signaled
// (even when the lock was already removed) so the caller never blocks indefinitely.
func (m *Manager) handleRemove(
	ev managerEvent,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
) {
	state, ok := m.detachLock(ev.id, ErrLockReleased, locks, items, h, inflightRenew)
	if ok {
		// Driver.Release runs in a background goroutine so the manager loop is
		// not blocked on I/O. A timeout is applied so a hung backend cannot leak
		// the goroutine indefinitely. The result is sent to ev.resultCh so the
		// remove() caller can observe the outcome (nil = success, non-nil = I/O error).
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), m.cfg.releaseTimeout)
			defer cancel()
			err := m.driver.Release(ctx, state.key, state.token)
			if err != nil {
				slog.Warn("distlock: release I/O error (lock may linger until TTL)",
					"key", state.key,
					"error", err)
			}
			ev.resultCh <- err
		}()
	} else {
		// Lock was already removed (lost before release was called) — idempotent.
		// Signal nil so the caller unblocks immediately.
		ev.resultCh <- nil
	}
}

// handleOrphan processes an orphan event. Unlike handleRemove it does NOT call
// Driver.Release: the backend key is intentionally left in place and will expire
// naturally after ≤1×TTL. This hands the lock to a competitor within one TTL
// window without any backend I/O, so handleOrphan never blocks on reachability.
// ev.resultCh is always signaled (nil) so the orphan() caller unblocks immediately.
func (m *Manager) handleOrphan(
	ev managerEvent,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
	inflightRenew map[lockID]context.CancelFunc,
) {
	// detachLock removes the lock from the heap so renewal stops immediately.
	// No Driver call is made — backend key expires on its own.
	state, ok := m.detachLock(ev.id, ErrLockOrphaned, locks, items, h, inflightRenew)
	if ok {
		slog.Debug("distlock: lock orphaned", "key", state.key)
	}
	// Always signal nil; orphan is fire-and-forget from the caller's perspective
	// (no I/O result to return).
	ev.resultCh <- nil
}
