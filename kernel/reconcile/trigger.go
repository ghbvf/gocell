package reconcile

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// Trigger is the source of reconcile work: Start launches a producer that feeds
// Requests into the Loop's work queue until ctx is canceled. It is GoCell's
// minimal analog of controller-runtime's source.Source — the K8s informer /
// predicate machinery is dropped (GoCell has no control plane to watch), so the
// sink is a plain chan<- Request rather than a rate-limiting workqueue
// (deduplication / backoff live in the Loop, not the Trigger).
//
// Contract:
//   - Start MUST be non-blocking: spawn the producer goroutine and return. It
//     is invoked once by the Loop (later: the Builder) with the Loop's internal
//     queue. (Mirrors source.Source's "Start must be non-blocking".)
//   - The producer MUST honor ctx: exit promptly on ctx.Done(), and never block
//     a queue send past cancellation (no goroutine leak on shutdown).
//   - The producer forwards with backpressure — it blocks on a full queue rather
//     than dropping (level-triggered: a coalesced signal is re-observed on the
//     next trigger / requeue, never silently lost).
//   - Start's error return is for genuine runtime startup failures. The two
//     minimal triggers below validate their configuration at CONSTRUCTION (a
//     misconfigured trigger is a programmer error, surfaced via panic — the
//     clock funnel + panic taxonomy convention), so their Start never fails; the
//     return is kept for source.Source parity and future triggers (e.g. one that
//     dials a broker) that can fail at startup.
//
// INVARIANT: RECONCILE-TRIGGER-INTERFACE-FROZEN-01 — the method set is frozen to
// exactly Start(context.Context, chan<- Request) error.
//
// ref: kubernetes-sigs/controller-runtime pkg/source/source.go
type Trigger interface {
	Start(ctx context.Context, queue chan<- Request) error
}

// tickerTrigger emits a zero-value resync pulse on every clock tick. It replaces
// controller-runtime's informer resync (which GoCell lacks): a context-free
// interval ticker has no entity to name, so it emits Request{} — a "re-observe
// everything you own" pulse the consumer's Reconcile fans out from.
type tickerTrigger struct {
	clk      clock.Clock
	interval time.Duration
}

// TickerTrigger returns a Trigger that emits a zero-value Request every interval,
// off the injected clock so the cadence is deterministically testable (advance a
// fake clock) rather than wall-clock-bound. clk and interval are required: a nil
// clock or non-positive interval is a programmer error and panics at
// construction (clock.MustHaveClock / clock.MustHavePositiveInterval) — clock is
// a mandatory positional dependency per CLOCK-POSITIONAL-INJECTION-01, not a
// WithClock option.
func TickerTrigger(clk clock.Clock, interval time.Duration) Trigger {
	clock.MustHaveClock(clk, "reconcile.TickerTrigger")
	clock.MustHavePositiveInterval(interval, "reconcile.TickerTrigger")
	return &tickerTrigger{clk: clk, interval: interval}
}

// Start creates the ticker synchronously (before spawning the producer
// goroutine), so a caller — or a test — that advances the injected clock
// immediately after Start deterministically observes the first tick: there is no
// race against the goroutine's first scheduling. It is non-blocking and never
// returns a non-nil error (config is validated at construction; see Trigger).
func (t *tickerTrigger) Start(ctx context.Context, queue chan<- Request) error {
	ticker := t.clk.NewTicker(t.interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C():
				select {
				case queue <- Request{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return nil
}

// channelTrigger forwards Requests from an external source channel into the work
// queue. It is the analog of controller-runtime's source.Channel — the wakeup
// path for event-driven reconcile (e.g. an outbox consumer pushing entity IDs).
type channelTrigger struct {
	in <-chan Request
}

// ChannelTrigger returns a Trigger that forwards every Request from in into the
// work queue (block-don't-drop). A nil source channel would block forever and is
// a programmer error: it panics at construction. When in is closed, the
// forwarding goroutine exits cleanly — the Loop keeps running but receives no
// further Requests from this Trigger (the source is responsible for its own
// lifecycle, mirroring Loop.pump over a closed Source).
func ChannelTrigger(in <-chan Request) Trigger {
	if in == nil {
		panic(panicregister.Approved("reconcile-channel-trigger-nil-source",
			errcode.Assertion("reconcile.ChannelTrigger: source channel must not be nil")))
	}
	return &channelTrigger{in: in}
}

func (t *channelTrigger) Start(ctx context.Context, queue chan<- Request) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-t.in:
				if !ok {
					return // source closed — no more work to forward
				}
				select {
				case queue <- req:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return nil
}

var (
	_ Trigger = (*tickerTrigger)(nil)
	_ Trigger = (*channelTrigger)(nil)
)
