package devicecmd

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/command"
)

// notifierBufferSize is the per-subscriber channel buffer capacity.
//
// Drop-on-full rationale: the Notifier is best-effort. The on-connect snapshot
// (ScanActive) is the authoritative catch-up path; the tail only delivers
// commands enqueued AFTER the watch opened. A subscriber that cannot keep up
// with delivery pace will lose tail events but will catch up on reconnect.
// Crucially, Notify MUST NOT block — a slow watching device must never stall
// Enqueue (the command ingestion path). Dropping is the correct fallback here;
// the no-op rationale is documented per go-standards.md.
const notifierBufferSize = 16

// subscriber holds the channel and a one-shot cancel guard for a single Watch
// subscription.
type subscriber struct {
	ch     chan command.Entry
	once   sync.Once
	parent *Notifier
	id     string // deviceID this subscriber listens on
	key    uint64 // monotone key for O(1) removal
}

// Notifier is a concurrency-safe in-process fan-out hub: callers Subscribe by
// deviceID, receive new command.Entry values from Notify. It is designed for
// the WatchCommands gRPC handler pattern where one or more streams may be open
// per device simultaneously.
//
// Lifecycle: construct once per cell (NewNotifier), pass to all enqueue-capable
// Service instances via WithOnEnqueue, and pass the same instance to NewServer
// so the gRPC handler can subscribe. See cell.go for the single shared
// construction.
type Notifier struct {
	mu   sync.Mutex
	subs map[string][]*subscriber // deviceID → ordered list
	next uint64                   // monotone key counter
}

// NewNotifier creates a ready-to-use Notifier.
func NewNotifier() *Notifier {
	return &Notifier{
		subs: make(map[string][]*subscriber),
	}
}

// Subscribe registers a new subscriber for deviceID. It returns a buffered
// receive channel and a cancel func. The cancel func is safe to call multiple
// times (sync.Once guard) and unsubscribes the channel from future Notify
// deliveries. The channel is never closed by the Notifier itself — consumers
// should select on the channel AND on ctx.Done().
func (n *Notifier) Subscribe(deviceID string) (<-chan command.Entry, func()) {
	n.mu.Lock()
	n.next++
	s := &subscriber{
		ch:     make(chan command.Entry, notifierBufferSize),
		parent: n,
		id:     deviceID,
		key:    n.next,
	}
	n.subs[deviceID] = append(n.subs[deviceID], s)
	n.mu.Unlock()

	cancel := func() {
		s.once.Do(func() {
			n.mu.Lock()
			defer n.mu.Unlock()
			list := n.subs[s.id]
			filtered := list[:0]
			for _, sub := range list {
				if sub.key != s.key {
					filtered = append(filtered, sub)
				}
			}
			if len(filtered) == 0 {
				delete(n.subs, s.id)
			} else {
				n.subs[s.id] = filtered
			}
		})
	}
	return s.ch, cancel
}

// Notify sends e to every active subscriber for e.DeviceID. If a subscriber's
// buffer is full the delivery is dropped (best-effort — see notifierBufferSize
// comment). Notify never blocks on a slow subscriber so that Enqueue is never
// stalled by a watching device.
func (n *Notifier) Notify(_ context.Context, e command.Entry) {
	n.mu.Lock()
	list := n.subs[e.DeviceID]
	if len(list) == 0 {
		n.mu.Unlock()
		return
	}
	// Copy the slice under lock so we can release before sending (avoids
	// holding the mutex during channel ops).
	targets := make([]*subscriber, len(list))
	copy(targets, list)
	n.mu.Unlock()

	for _, s := range targets {
		select {
		case s.ch <- e:
		default:
			// Buffer full — drop silently (best-effort, documented above).
		}
	}
}
