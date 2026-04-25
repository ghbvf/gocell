package distlock

import (
	"container/heap"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// lockID is a monotonically incrementing identifier for active lock entries.
type lockID = uint64

// lockState holds the runtime state for a single active lock.
type lockState struct {
	id     lockID
	key    string
	token  string
	ttl    time.Duration
	cancel context.CancelCauseFunc
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

// eventKind distinguishes add and remove events sent to the manager.
type eventKind int

const (
	eventAdd    eventKind = iota
	eventRemove           // initiated by release()
)

// managerEvent carries a single instruction to the manager goroutine.
type managerEvent struct {
	kind  eventKind
	state *lockState // eventAdd: the new lock to register
	id    lockID     // eventRemove: lock to unregister
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

	// mu protects running, started, drained, stopCh, and snapshotLocks.
	// The heap/locks/items are owned exclusively by the run() goroutine.
	mu            sync.Mutex
	running       bool
	started       chan struct{}
	drained       chan struct{}
	stopCh        chan struct{}
	snapshotLocks int // maintained by run() via atomic-ish updates under mu

	nextID atomic.Uint64
	// pendingReleases counts how many locks have been added but whose
	// corresponding remove() call has not yet been processed.  The manager
	// drains only when this reaches zero via an eventRemove event.
	// Protected by mu (written by add/run; read by run).
	pendingReleases int
	events          chan managerEvent

	// releaseWg tracks in-flight background Driver.Release goroutines.
	// The Drained() channel closes only after all release goroutines complete,
	// so callers waiting on Drained() are guaranteed Driver.Release has run
	// for every lock that called release() during this manager lifecycle.
	releaseWg sync.WaitGroup

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
		stopCh:      make(chan struct{}),
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
// after the last lock is released.
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
	m.pendingReleases++
	if !m.running {
		m.running = true
		// Fresh channels for this manager lifecycle.
		m.started = make(chan struct{})
		m.drained = make(chan struct{})
		m.stopCh = make(chan struct{})
		go m.run()
	}
	m.mu.Unlock()

	m.events <- managerEvent{kind: eventAdd, state: state}
}

// remove asks the manager to release a lock. It is fire-and-forget: the event
// is sent and remove returns immediately. The actual Driver.Release I/O runs
// asynchronously in a background goroutine. Callers that need to wait for all
// in-flight releases to complete should use Manager.Drained().
//
// Idempotent: the sync.Once in the Acquire closure ensures remove is called at
// most once per lock.
func (m *Manager) remove(id lockID) {
	m.events <- managerEvent{kind: eventRemove, id: id}
}

// run is the manager's main goroutine. It must not be called directly.
// It is the sole writer of locks, h, and items.
func (m *Manager) run() {
	locks := make(map[lockID]*lockState)
	items := make(map[lockID]*heapItem)
	var h renewHeap
	heap.Init(&h)

	slog.Debug("distlock: manager started")
	close(m.started)

	for {
		timer, timerC := m.nextTimer(&h)
		done := m.runOnce(timer, timerC, locks, items, &h)
		if done {
			return
		}
	}
}

// nextTimer returns a Timer (and its channel) for the earliest heap item,
// or nil/nil if the heap is empty.
func (m *Manager) nextTimer(h *renewHeap) (Timer, <-chan time.Time) {
	if h.Len() == 0 {
		return nil, nil
	}
	d := h.Peek().nextRenew.Sub(m.cfg.clock.Now())
	if d < 0 {
		d = 0
	}
	t := m.cfg.clock.NewTimer(d)
	return t, t.C()
}

// runOnce executes a single iteration of the manager's select loop.
// Returns true when the manager should exit.
func (m *Manager) runOnce(
	timer Timer,
	timerC <-chan time.Time,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
) bool {
	select {
	case <-timerC:
		m.handleRenew(locks, items, h)
	case ev := <-m.events:
		if timer != nil {
			// Stop returns; no drain needed because we never reuse the timer object —
			// a fresh one is created next iteration. Future refactors using Reset must
			// add a drain-on-false guard here.
			timer.Stop()
		}
		if m.dispatchEvent(ev, locks, items, h) {
			return true
		}
	case <-m.stopCh:
		if timer != nil {
			// Stop returns; no drain needed because we never reuse the timer object —
			// a fresh one is created next iteration. Future refactors using Reset must
			// add a drain-on-false guard here.
			timer.Stop()
		}
		return true
	}
	return false
}

// dispatchEvent handles a single manager event. Returns true when the manager
// should drain and exit (last lock released).
func (m *Manager) dispatchEvent(
	ev managerEvent,
	locks map[lockID]*lockState,
	items map[lockID]*heapItem,
	h *renewHeap,
) bool {
	switch ev.kind {
	case eventAdd:
		m.handleAdd(ev.state, locks, items, h)
	case eventRemove:
		m.handleRemove(ev, locks, items, h)
		// Decrement the pending-releases counter. Each add() increments
		// it; only the explicit remove() path decrements it. This ensures
		// the manager stays alive until release() is called for every lock,
		// even if some locks are already lost via renewal failure.
		m.mu.Lock()
		m.pendingReleases--
		pending := m.pendingReleases
		m.mu.Unlock()
		if pending == 0 {
			// Wait for all background Driver.Release goroutines to complete
			// before closing drained, so callers of Drained() are guaranteed
			// that every release() call has had its I/O flushed.
			m.releaseWg.Wait()
			m.mu.Lock()
			m.running = false
			m.snapshotLocks = 0
			drained := m.drained
			m.mu.Unlock()
			slog.Debug("distlock: manager drained")
			close(drained)
			return true
		}
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

// handleRenew pops the earliest item, calls Driver.Renew, and re-queues or cancels.
func (m *Manager) handleRenew(locks map[lockID]*lockState, items map[lockID]*heapItem, h *renewHeap) {
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
	// same time domain as the FakeClock in tests, and aligns with the WithDriftFactor
	// documentation that defines the margin relative to the backend TTL.
	// ref: plan "Driver renew 调用本身的超时" — deadline = clock.Now() + ttl - drift
	renewTimeout := ttl - drift
	var renewCtx context.Context
	var cancel context.CancelFunc
	if renewTimeout > 0 {
		deadline := m.cfg.clock.Now().Add(renewTimeout)
		renewCtx, cancel = context.WithDeadline(context.Background(), deadline)
		defer cancel()
	} else {
		renewCtx = context.Background()
	}

	held, err := m.driver.Renew(renewCtx, state.key, state.token, ttl)
	if err != nil {
		slog.Error("distlock: renewal I/O error; lock lost",
			"key", state.key,
			"op", "Renew",
			"ttl", state.ttl,
			"error", err)
		state.cancel(ErrLockLost)
		delete(locks, item.id)
		m.mu.Lock()
		m.snapshotLocks = len(locks)
		m.mu.Unlock()
		return
	}
	if !held {
		slog.Error("distlock: renewal ownership lost",
			"key", state.key,
			"op", "Renew",
			"ttl", state.ttl)
		state.cancel(ErrLockLost)
		delete(locks, item.id)
		m.mu.Lock()
		m.snapshotLocks = len(locks)
		m.mu.Unlock()
		return
	}

	// Re-queue: schedule the next renew at now + ttl * renewFraction.
	// The renewFraction-based schedule ensures timely renewal regardless of drift.
	// ref: plan main loop — "requeue 用 driver 实际成功时间 + ttl × renewFraction"
	item.nextRenew = m.cfg.clock.Now().Add(time.Duration(float64(ttl) * m.cfg.renewFraction))
	item.index = -1
	items[item.id] = item
	heap.Push(h, item)

	// Signal renewNotify so tests can synchronize on renew completion.
	select {
	case m.renewNotify <- struct{}{}:
	default:
	}
}

// handleRemove processes a remove event. The event is fire-and-forget:
// Driver.Release runs in a background goroutine and handleRemove returns
// immediately so the manager loop is not blocked on I/O. Callers that need
// to synchronize on release completion should use Manager.Drained().
func (m *Manager) handleRemove(ev managerEvent, locks map[lockID]*lockState, items map[lockID]*heapItem, h *renewHeap) {
	state, ok := locks[ev.id]
	if ok {
		delete(locks, ev.id)
		if item, has := items[ev.id]; has {
			heap.Remove(h, item.index)
			delete(items, ev.id)
		}
		m.mu.Lock()
		m.snapshotLocks = len(locks)
		m.mu.Unlock()
	}

	if ok {
		state.cancel(ErrLockReleased)
		// Driver.Release runs in a background goroutine so the manager loop
		// is not blocked on I/O. A timeout is applied so a hung backend cannot
		// leak the goroutine indefinitely. releaseWg is decremented when done
		// so Drained() waits for all in-flight releases to complete.
		m.releaseWg.Add(1)
		go func() {
			defer m.releaseWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), m.cfg.releaseTimeout)
			defer cancel()
			if err := m.driver.Release(ctx, state.key, state.token); err != nil {
				slog.Warn("distlock: release I/O error (lock may linger until TTL)",
					"key", state.key,
					"error", err)
			}
		}()
	}
	// If !ok the lock was already removed (lost before release was called) — no-op.
}
