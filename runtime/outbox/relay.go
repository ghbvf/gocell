package outbox

import (
	"context"
	"errors"
	"log/slog"

	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used // non-crypto outbox relay jitter; gosec G404 already silenced at usage sites
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/observability"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/worker"
)

// ProbePoll / ProbeReclaim / ProbeCleanup are the typed healthz.ProbeName
// consts for the three relay failure-budget probes. They participate in the
// PROBENAME-SEALED-FUNNEL-01 typed funnel so the composition root no longer
// translates bare strings from ManagedResource — the names flow as typed
// const all the way from Relay.Probes() to /readyz registration. Wire values
// are preserved (no "_ready" suffix; relay budgets are not adapter
// dependency-availability probes — see adapterSanctionedPkgs convention).
const (
	ProbePoll    healthz.ProbeName = "outbox_relay_poll"
	ProbeReclaim healthz.ProbeName = "outbox_relay_reclaim"
	ProbeCleanup healthz.ProbeName = "outbox_relay_cleanup"
)

const commandReceiptSettleTimeout = 5 * time.Second

// Compile-time interface checks.
//
// *Relay intentionally does NOT implement kernel/lifecycle.ManagedResource: see
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md and
// runtime/bootstrap/relay_adapter.go. WithManagedResource(relay) is now a
// compile-time type-mismatch (Hard).
var (
	_ kout.Relay    = (*Relay)(nil)
	_ worker.Worker = (*Relay)(nil)
)

// ---------------------------------------------------------------------------
// Relay lifecycle state machine
// ---------------------------------------------------------------------------

type relayState int32

const (
	relayStopped  relayState = iota // zero value = stopped
	relayStarting                   // Start() entered, goroutines launching
	relayRunning                    // poll/cleanup/reclaim loops active
	relayStopping                   // Stop() called, waiting for goroutines
)

// ---------------------------------------------------------------------------
// Internal relay error codes
// ---------------------------------------------------------------------------

const (
	// errRelayOp is used for lifecycle (start/stop) errors.
	errRelayOp errcode.Code = "ERR_OUTBOX_RELAY_OP"
)

// ---------------------------------------------------------------------------
// publishResult records the outcome of publishing a single entry.
// ---------------------------------------------------------------------------

type publishResult struct {
	entry ClaimedEntry
	err   error
	// receipt is the live idempotency lease handle for a command entry that was
	// dispatched in-process under a freshly acquired claim (ClaimAcquired). It is
	// nil for events, for non-acquired command claims (ClaimDone / ClaimBusy /
	// missing-identity / Claim-infra-error), and for any path that did not take the
	// command branch. writeBack Commits it on success and Releases it on failure;
	// nil receipts are skipped (NonAcquiredReceipt is deliberately not stored here).
	receipt idempotency.Receipt
}

// pollStats records per-poll-cycle counters for observability.
type pollStats struct {
	published int
	retried   int
	dead      int
	skipped   int
	// lost counts failure writebacks that lost their lease mid-flight
	// (Mark{Retry,Dead} returned updated=false). The new lease owner — the
	// reclaimer or a peer — reports the canonical outcome, so this writeback
	// must NOT be counted as retried/dead. ref: B2-A-05.
	lost int
}

// ---------------------------------------------------------------------------
// Relay
// ---------------------------------------------------------------------------

// PendingDepthObserver receives the current pending-entry count once per
// reclaim cycle. The production implementation is
// [runtime/observability/metrics.OutboxPendingDepthCollector]; tests may use a
// simple func adapter. A nil observer is silently ignored (no-op).
//
// Intentionally defined here (not imported from runtime/observability/metrics)
// to avoid coupling runtime/outbox to its sibling package.
type PendingDepthObserver interface {
	ObservePendingDepth(ctx context.Context, n int64)
}

// Relay polls unpublished outbox entries via a Store interface and publishes
// them via the provided outbox.Publisher using a three-phase approach:
//
//	Phase 1 (claim):     Store.ClaimPending — short tx in the Store impl
//	Phase 2 (publish):   outside tx — publish each entry to broker
//	Phase 3 (writeBack): Store.MarkPublished / MarkRetry / MarkDead — short tx each
//
// Consistency level: L2 (OutboxFact)
//
// Outbox entry state machine:
//
//	pending ──claim──→ claiming ──publish ok──→ published ──retention──→ (deleted)
//	   ↑                  │
//	   │ (fail, attempts < max)
//	   └──────────────────┘
//	                      │ (fail, attempts >= max)
//	                      ↓
//	                     dead ──dead retention──→ (deleted)
//
// ReclaimStale: claiming entries past ClaimTTL are recovered with attempts++.
// If attempts reaches MaxAttempts during reclaim, the entry is marked dead.
//
// ref: Watermill router.go — goroutine-per-handler lifecycle pattern
type Relay struct {
	store   Store
	pub     kout.Publisher
	cfg     RelayConfig
	metrics kout.RelayCollector

	// state is the lifecycle state machine (atomic for lock-free reads).
	state atomic.Int32

	// mu protects lifecycle state shared by Start and Stop.
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{} // closed once Start() transitions to relayRunning

	wg sync.WaitGroup

	// Failure budgets for each background loop. nil means disabled (threshold=0).
	// ref: K8s workqueue ItemExponentialFailureRateLimiter — absolute count + Forget.
	pollBudget    *FailureBudget
	reclaimBudget *FailureBudget
	cleanupBudget *FailureBudget

	// pendingDepthObserver, when non-nil, receives the pending entry count once
	// per reclaim tick. Nil means "do not observe" (opt-in via
	// WithPendingDepthObserver).
	pendingDepthObserver PendingDepthObserver

	clock clock.Clock

	// cmdRegistry + cmdDispatch wire the in-process async command bus into the
	// relay (#1667 / ADR ...202606040550-1044 §5 ④). When a claimed entry's
	// RoutingTopic matches a registered command id, the relay dispatches it to
	// the generated DispatchAsync in-process (settling the row like a publish)
	// instead of marshaling + publishing to the broker. Both are nil unless
	// WithCommandDispatch was called, so event-only relays are unaffected. See
	// relay_command.go.
	cmdRegistry *command.Registry
	cmdDispatch map[command.CommandID]command.AsyncDispatchFunc

	// cmdClaimer wraps every in-process command dispatch in the two-phase
	// idempotency protocol (Claim → dispatch → Commit/Release) so an at-least-once
	// source-event redelivery cannot enqueue the same command twice (#1698). It is
	// REQUIRED whenever cmdDispatch is non-empty; Start fails fast on a nil claimer
	// with a non-empty dispatch map.
	cmdClaimer idempotency.Claimer
}

// WithPendingDepthObserver wires a PendingDepthObserver that receives the
// pending-entry count once per reclaim cycle. Both bare-nil and typed-nil
// inputs are silently ignored (no observer stored); the relay operates without
// observation when no observer is set. Must be called before Start().
func (r *Relay) WithPendingDepthObserver(o PendingDepthObserver) *Relay {
	if validation.IsNilInterface(o) {
		return r
	}
	r.pendingDepthObserver = o
	return r
}

// clk returns the relay's clock.
func (r *Relay) clk() clock.Clock {
	return r.clock
}

// NewRelay creates a Relay that polls from store and publishes via pub.
// Zero or negative cfg values are replaced with defaults via cfg.WithDefaults().
// A nil Metrics is replaced with NoopRelayCollector; the collector is then
// wrapped in safeRelayCollector so panics cannot crash relay goroutines.
func NewRelay(clk clock.Clock, store Store, pub kout.Publisher, cfg RelayConfig) *Relay {
	clock.MustHaveClock(clk, "outbox.NewRelay")
	cfg = cfg.WithDefaults()
	if cfg.Metrics == nil {
		cfg.Metrics = kout.NoopRelayCollector{}
	}
	// Wrap in safe adapter: collector panics must not crash relay goroutines.
	// ref: runtime/http/middleware/safe_observe.go — same pattern for HTTP metrics.
	metrics := &safeRelayCollector{inner: cfg.Metrics}

	// Guard: ClaimTTL must exceed 2x PollInterval to prevent ReclaimStale
	// from reclaiming entries still being processed.
	if cfg.ClaimTTL <= cfg.PollInterval*claimTTLPollMultiplier {
		slog.Warn("outbox relay: ClaimTTL should be > 2*PollInterval to avoid premature reclaim",
			slog.Duration("claim_ttl", cfg.ClaimTTL),
			slog.Duration("poll_interval", cfg.PollInterval))
	}

	r := &Relay{
		store:   store,
		pub:     pub,
		cfg:     cfg,
		metrics: metrics,
		readyCh: make(chan struct{}),
		clock:   clk,
	}
	// Instantiate failure budgets. threshold=0 → nil (disabled).
	if cfg.PollFailureBudget > 0 {
		r.pollBudget = NewFailureBudget(string(ProbePoll), cfg.PollFailureBudget)
	}
	if cfg.ReclaimFailureBudget > 0 {
		r.reclaimBudget = NewFailureBudget(string(ProbeReclaim), cfg.ReclaimFailureBudget)
	}
	if cfg.CleanupFailureBudget > 0 {
		r.cleanupBudget = NewFailureBudget(string(ProbeCleanup), cfg.CleanupFailureBudget)
	}
	return r
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins the relay polling loop, cleanup goroutine, and reclaim loop.
// It blocks until ctx is canceled or Stop is called.
func (r *Relay) Start(ctx context.Context) error {
	if !r.state.CompareAndSwap(int32(relayStopped), int32(relayStarting)) {
		return errcode.New(errcode.KindConflict, errRelayOp, "outbox relay already started")
	}

	// Fail-closed: command dispatch must never run without a Claimer (#1698).
	// A non-empty dispatch map with a nil claimer would dispatch commands with no
	// deduplication, letting an at-least-once source event enqueue the same command
	// twice. Reset state so a corrected re-Start is possible.
	if len(r.cmdDispatch) > 0 && validation.IsNilInterface(r.cmdClaimer) {
		r.state.Store(int32(relayStopped))
		return errcode.New(errcode.KindInvalid, errRelayOp,
			"outbox relay: command dispatch wired without claimer")
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	r.mu.Lock()
	r.cancel = cancel
	r.done = done
	r.wg.Add(3)
	r.mu.Unlock()

	// Reset budgets so stale trip state from a previous run does not bleed into
	// the new run.  Must happen after CAS (exclusive) and before goroutines start.
	if r.pollBudget != nil {
		r.pollBudget.Reset()
	}
	if r.reclaimBudget != nil {
		r.reclaimBudget.Reset()
	}
	if r.cleanupBudget != nil {
		r.cleanupBudget.Reset()
	}

	r.state.Store(int32(relayRunning))
	close(r.readyCh)

	defer func() {
		r.wg.Wait()

		r.mu.Lock()
		r.cancel = nil
		r.done = nil
		r.readyCh = make(chan struct{}) // fresh open channel; next Start() will close it
		r.state.Store(int32(relayStopped))
		close(done)
		r.mu.Unlock()
	}()

	go func() {
		defer r.wg.Done()
		r.pollLoop(ctx)
	}()

	go func() {
		defer r.wg.Done()
		r.cleanupLoop(ctx)
	}()

	go func() {
		defer r.wg.Done()
		r.reclaimLoop(ctx)
	}()

	slog.Info(
		"outbox relay: started",
		slog.Duration("poll_interval", r.cfg.PollInterval),
		slog.Int("batch_size", r.cfg.BatchSize),
		slog.Int("max_attempts", r.cfg.MaxAttempts),
		slog.Duration("claim_ttl", r.cfg.ClaimTTL),
	)

	<-ctx.Done()
	return nil
}

// Stop signals the relay to shut down gracefully and waits for goroutines.
// It respects the caller's context deadline: if ctx expires before goroutines
// finish, Stop returns an error instead of blocking indefinitely.
// Stop is fully idempotent: calling it multiple times (e.g. via both
// ManagedResource.Close and WorkerGroup.Stop) is safe and returns nil on every
// call after the relay is already stopping or stopped.
func (r *Relay) Stop(ctx context.Context) error {
	// If never started, already stopped, or already stopping — no-op.
	// cancel is only set during an active Start() call and cleared by the
	// Start() defer on shutdown.
	r.mu.Lock()
	state := relayState(r.state.Load())
	notStarted := r.cancel == nil && state == relayStopped
	alreadyStopping := state == relayStopping
	ready := r.readyCh
	r.mu.Unlock()

	if notStarted || alreadyStopping {
		return nil
	}

	// Wait for Start() to transition to relayRunning.
	select {
	case <-ready:
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errRelayOp, "relay stop: timed out waiting for start", ctx.Err())
	}

	r.state.Store(int32(relayStopping))

	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}

	select {
	case <-done:
		slog.Info("outbox relay: stopped")
		return nil
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errRelayOp, "relay stop timeout", ctx.Err())
	}
}

// ---------------------------------------------------------------------------
// Background loops
// ---------------------------------------------------------------------------

// pollLoop fetches unpublished entries and publishes them.
func (r *Relay) pollLoop(ctx context.Context) {
	ticker := r.clk().NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			err := r.pollOnce(ctx)
			if err != nil {
				slog.Warn(
					"outbox relay: poll failed",
					slog.Any("error", err),
				)
			}
			if r.pollBudget != nil {
				r.pollBudget.Record(err)
			}
		}
	}
}

// reclaimLoop periodically runs reclaimStale at ReclaimInterval, then probes
// pending depth when a PendingDepthObserver is configured.
func (r *Relay) reclaimLoop(ctx context.Context) {
	ticker := r.clk().NewTicker(r.cfg.ReclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			err := r.reclaimStale(ctx)
			if err != nil {
				slog.Warn(
					"outbox relay: reclaim failed",
					slog.Any("error", err),
				)
			}
			if r.reclaimBudget != nil {
				r.reclaimBudget.Record(err)
			}
			// Note: pending depth is sampled once per ReclaimInterval, not per
			// Prometheus scrape. The outbox_pending_depth Gauge reflects depth
			// at last reclaim tick; decrease ReclaimInterval for tighter sampling.
			r.observePendingDepth(ctx)
		}
	}
}

// observePendingDepth calls Store.CountPending and forwards the result to the
// configured PendingDepthObserver, if any. Errors are logged as Warn and do
// not propagate — this is a best-effort observability call on a non-hot path.
func (r *Relay) observePendingDepth(ctx context.Context) {
	if r.pendingDepthObserver == nil {
		return
	}
	n, err := r.store.CountPending(ctx)
	if err != nil {
		slog.Warn("outbox relay: CountPending failed, skipping pending_depth metric",
			slog.Any("error", err))
		return
	}
	observability.SafeObserve(slog.Default(), func() { r.pendingDepthObserver.ObservePendingDepth(ctx, n) })
}

// cleanupLoop runs cleanup data-driven: after each pass it asks the store for
// the oldest published / dead row and sleeps exactly until that row crosses its
// retention window, instead of polling on a fixed timer. Bounded by [floor,
// ceiling] for safety: floor prevents tight-loop on clock skew; ceiling acts as
// a periodic re-check in case OldestEligibleAt itself returns stale info or no
// rows exist for a long time.
func (r *Relay) cleanupLoop(ctx context.Context) {
	for {
		err := r.cleanup(ctx)
		if err != nil {
			slog.Warn("outbox relay: cleanup failed", slog.Any("error", err))
		}
		if r.cleanupBudget != nil {
			r.cleanupBudget.Record(err)
		}

		wait := r.nextCleanupWait(ctx)
		t := r.clk().NewTimerAt(r.clk().Now().Add(wait))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
			t.Stop()
		}
	}
}

// defaultCleanupWaitFloor and cleanupWaitCeiling bound the cleanup wake-up sleep.
// Floor avoids tight-loop on clock skew. Ceiling forces a periodic re-check
// even when the table is empty (OldestEligibleAt returns ok=false).
const (
	defaultCleanupWaitFloor = 5 * time.Second
	cleanupWaitCeiling      = 1 * time.Hour
)

// claimTTLPollMultiplier is the minimum ratio of ClaimTTL to PollInterval:
// ClaimTTL must exceed PollInterval * claimTTLPollMultiplier to prevent
// ReclaimStale from reclaiming entries still being processed.
const claimTTLPollMultiplier = 2

// nextCleanupWait computes how long the cleanup loop should sleep before the
// next pass: min(time-until-next-published-eligible, time-until-next-dead-eligible),
// clamped to [floor, ceiling]. Returns ceiling when the store reports no
// candidates for either status (idle table) or any error (defensive: keep
// the loop alive but back off).
func (r *Relay) nextCleanupWait(ctx context.Context) time.Duration {
	now := r.clk().Now()
	wait := cleanupWaitCeiling

	if pubAt, ok := r.oldestOrZero(ctx, kout.StatePublished); ok {
		if d := pubAt.Add(r.cfg.RetentionPeriod).Sub(now); d < wait {
			wait = d
		}
	}
	if deadAt, ok := r.oldestOrZero(ctx, kout.StateDead); ok {
		if d := deadAt.Add(r.cfg.DeadRetentionPeriod).Sub(now); d < wait {
			wait = d
		}
	}

	floor := r.cleanupWaitFloor()
	if wait < floor {
		wait = floor
	}
	return wait
}

// cleanupWaitFloor returns the effective cleanup floor duration:
// cfg.CleanupWaitFloor if positive, otherwise defaultCleanupWaitFloor.
func (r *Relay) cleanupWaitFloor() time.Duration {
	if r.cfg.CleanupWaitFloor > 0 {
		return r.cfg.CleanupWaitFloor
	}
	return defaultCleanupWaitFloor
}

// oldestOrZero wraps Store.OldestEligibleAt with logging and an idle-fallback:
// any error degrades to ok=false so nextCleanupWait falls back to the ceiling
// instead of tight-looping.
func (r *Relay) oldestOrZero(ctx context.Context, status kout.State) (time.Time, bool) {
	at, ok, err := r.store.OldestEligibleAt(ctx, status)
	if err != nil {
		slog.Warn(
			"outbox relay: OldestEligibleAt failed, backing off to ceiling",
			slog.String("status", status.String()),
			slog.Any("error", err),
		)
		return time.Time{}, false
	}
	return at, ok
}

// ---------------------------------------------------------------------------
// pollOnce — three-phase relay cycle
// ---------------------------------------------------------------------------

// pollOnce executes the three-phase relay cycle: claim → publish → writeBack.
func (r *Relay) pollOnce(ctx context.Context) error {
	start := r.clk().Now()

	// Phase 1: Claim
	entries, err := r.store.ClaimPending(ctx, r.cfg.BatchSize)
	if err != nil {
		return err
	}

	// Record batch size even for empty batches (captures idle cycles).
	r.metrics.RecordBatchSize(ctx, len(entries))

	if len(entries) == 0 {
		return nil
	}
	claimDur := r.clk().Since(start)

	// Phase 2: Publish (outside tx)
	pubStart := r.clk().Now()
	results := r.publishBatch(ctx, entries)
	pubDur := r.clk().Since(pubStart)

	// Phase 3: WriteBack
	wbStart := r.clk().Now()
	stats, wbErr := r.writeBackResults(ctx, results)
	wbDur := r.clk().Since(wbStart)

	// Log and record metrics only after writeBack completes — if commit
	// fails, stats are rolled back and recording them would be misleading.
	if wbErr == nil {
		slog.Info(
			"outbox relay: poll complete",
			slog.Int("published", stats.published),
			slog.Int("retried", stats.retried),
			slog.Int("dead_lettered", stats.dead),
			slog.Int("skipped", stats.skipped),
			slog.Int("lost", stats.lost),
			slog.Duration("claim_dur", claimDur),
			slog.Duration("publish_dur", pubDur),
		)
		r.metrics.RecordPollCycle(ctx, kout.PollCycleResult{
			Published:    stats.published,
			Retried:      stats.retried,
			Dead:         stats.dead,
			Skipped:      stats.skipped,
			Lost:         stats.lost,
			ClaimDur:     claimDur,
			PublishDur:   pubDur,
			WriteBackDur: wbDur,
		})
	}

	return wbErr
}

// publishBatch delivers each entry to its sink outside of any transaction. The
// sink is the broker for events (marshal envelope → Publisher.Publish) and the
// in-process command handler for command entries whose RoutingTopic matches a
// registered command id (dispatch → generated DispatchAsync; see
// WithCommandDispatch in relay_command.go). Both outcomes settle through the
// shared writeBack below (MarkPublished on success / MarkRetry on error), so a
// dispatched command's terminal state is "published" — the row is consumed.
// Uses kernel/outbox.MarshalEnvelope to produce the wire envelope with camelCase
// JSON keys.
// ref: Watermill router.go publishBatch — per-message outcome, no batch atomicity
func (r *Relay) publishBatch(ctx context.Context, entries []ClaimedEntry) []publishResult {
	results := make([]publishResult, len(entries))
	for i, e := range entries {
		if fn, ok := r.commandDispatchFor(e.RoutingTopic()); ok {
			// Command entry: dispatch to its in-process handler (wrapped in the
			// two-phase Claimer protocol) instead of publishing to the broker.
			results[i] = r.dispatchCommand(ctx, e, fn)
			continue
		}
		payload, marshalErr := kout.MarshalEnvelope(e.Entry)
		if marshalErr != nil {
			// MarshalEnvelope failure is deterministic (the entry can never encode):
			// tag it permanent so handleFailedEntry dead-letters it immediately
			// instead of retrying to budget exhaustion.
			results[i] = publishResult{entry: e, err: kout.NewPermanentError(marshalErr)}
			continue
		}
		results[i] = publishResult{
			entry: e,
			err:   r.pub.Publish(ctx, e.RoutingTopic(), payload),
		}
	}
	return results
}

// commandLeaseTTL / commandDoneTTL are the processing-lease and done-key TTLs the
// relay passes to the command Claimer. They reuse the framework idempotency
// defaults (5m lease / 24h done) — the lease covers a single in-process dispatch
// (sub-second) with generous headroom, and the done key dedupes redeliveries of
// the same source event across the standard 24h idempotency window. They are not
// exposed as public RelayConfig fields (no real tuning need today, YAGNI).
const (
	commandLeaseTTL = idempotency.DefaultLeaseTTL // 5m
	commandDoneTTL  = idempotency.DefaultTTL      // 24h
)

// dispatchCommand wraps a single in-process command dispatch in the two-phase
// Claimer protocol (#1698). Extracted from publishBatch to keep that loop under
// the cognitive-complexity ceiling. The returned publishResult settles through
// the shared writeBack:
//
//   - missing identity → permanent error → MarkDead (fail-closed; an entry with no
//     idempotency identity cannot be safely deduplicated).
//   - Claim infra error → transient error → MarkRetry (no permanent tag).
//   - ClaimAcquired → dispatch + carry the live receipt (Commit on success,
//     Release on failure).
//   - ClaimDone → already processed → success without dispatch (command deduped).
//   - ClaimBusy → another worker holds the claim → transient error → MarkRetry.
//   - unknown state → permanent error → MarkDead (fail-closed).
//
// Dedup observability is a deliberate design decision, not a metric gap: the
// ClaimDone (deduped) branch settles the row as published — the outbox row WAS
// consumed, so "published" is the correct row outcome — and the command-level
// dedup signal is carried by the "command deduped" Info log below (operators
// reconcile dedup rate from that log line's count, NOT from the outbox published
// counter, which deliberately does not distinguish first-dispatch from dedup).
// Each branch emits a structured slog line (entry_id / routing_topic /
// command_id / state / error — never the command payload body) so operators can
// distinguish dedup vs first-dispatch vs fail-closed dead-letter vs busy retry.
func (r *Relay) dispatchCommand(ctx context.Context, e ClaimedEntry, fn command.AsyncDispatchFunc) publishResult {
	key, ok := command.ClaimKeyFromEntry(e.Entry)
	if !ok {
		// Security-relevant fail-closed: an entry with no idempotency identity
		// cannot be safely deduplicated, so it is dead-lettered before Claim.
		// command_id is empty here (that is the defect), so it is not logged.
		slog.Error("outbox relay: command entry missing idempotency identity, dead-lettering",
			slog.String("entry_id", e.ID()),
			slog.String("routing_topic", e.RoutingTopic()))
		return publishResult{entry: e, err: kout.NewPermanentError(
			errcode.New(errcode.KindInvalid, errRelayOp,
				"outbox relay: command entry missing idempotency identity"),
		)}
	}

	state, receipt, err := r.cmdClaimer.Claim(ctx, key, commandLeaseTTL, commandDoneTTL)
	if err != nil {
		// Claim infrastructure failure is transient — retry, do not dead-letter.
		slog.Warn("outbox relay: command claim infra error, will retry",
			slog.String("entry_id", e.ID()),
			slog.String("routing_topic", e.RoutingTopic()),
			slog.Any("error", err))
		return publishResult{entry: e, err: err}
	}

	cmdID := e.Metadata()[command.CommandIDMetadataKey]
	switch state {
	case idempotency.ClaimAcquired:
		// fn is a generated DispatchAsync (COMMAND-ASYNC-DISPATCH-CALLER-01 locks
		// the map values). Carry the live receipt so writeBack settles the lease.
		//
		// Opt-in active-uniqueness: if the producer set CommandDeadlineMetadataKey,
		// parse it and inject (key, deadline) into ctx so the handler can read them
		// via command.DispatchedUniqueness. Absent key → no injection (zero-overhead
		// for commands that did not opt in). Corrupt deadline → fail-closed dead-letter
		// (mirror the missing-identity guard above; a corrupt deadline is a producer bug).
		dispatchCtx := ctx
		if dl := e.Metadata()[command.CommandDeadlineMetadataKey]; dl != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, dl)
			if parseErr != nil {
				// Cap raw_deadline to bound log size if a malformed value appears
				// (e.g. a very long string injected by a misbehaving producer).
				const rawDeadlineLogCap = 64
				rawSnippet := dl
				if len(rawSnippet) > rawDeadlineLogCap {
					rawSnippet = rawSnippet[:rawDeadlineLogCap]
				}
				slog.Error("outbox relay: command entry has unparseable deadline, dead-lettering",
					slog.String("entry_id", e.ID()),
					slog.String("routing_topic", e.RoutingTopic()),
					slog.String("command_id", cmdID),
					slog.String("raw_deadline", rawSnippet),
					slog.Any("error", parseErr))
				return publishResult{entry: e, err: kout.NewPermanentError(
					errcode.New(errcode.KindInvalid, errRelayOp,
						"outbox relay: command entry has unparseable overall_deadline"),
				)}
			}
			dispatchCtx = command.WithDispatchedUniqueness(ctx, key, parsed)
		}
		return publishResult{entry: e, err: fn(dispatchCtx, r.cmdRegistry, e.Entry), receipt: receipt}
	case idempotency.ClaimDone:
		// Already processed by an earlier delivery — skip dispatch, settle the row
		// as published (the command is deduped, not re-enqueued). No live receipt.
		slog.Info("outbox relay: command deduped (already processed)",
			slog.String("entry_id", e.ID()),
			slog.String("routing_topic", e.RoutingTopic()),
			slog.String("command_id", cmdID))
		return publishResult{entry: e}
	case idempotency.ClaimBusy:
		// Another worker holds the claim — transient, retry later.
		slog.Warn("outbox relay: command dispatch busy, another worker holds claim",
			slog.String("entry_id", e.ID()),
			slog.String("routing_topic", e.RoutingTopic()),
			slog.String("command_id", cmdID))
		return publishResult{entry: e, err: errCommandDispatchBusy}
	default:
		// Claimer returned a state outside the enumerated set — a state-machine
		// violation, fail-closed dead-letter.
		slog.Error("outbox relay: command claim returned unknown state, dead-lettering",
			slog.String("entry_id", e.ID()),
			slog.String("routing_topic", e.RoutingTopic()),
			slog.Int("claim_state", int(state)))
		return publishResult{entry: e, err: kout.NewPermanentError(
			errcode.New(errcode.KindInternal, errRelayOp,
				"outbox relay: command claim returned unknown state"),
		)}
	}
}

// errCommandDispatchBusy is the transient sentinel returned when the command
// Claimer reports ClaimBusy (another worker is mid-dispatch). It routes through
// the normal retry path (MarkRetry) so the entry is re-attempted later.
var errCommandDispatchBusy = errcode.New(errcode.KindConflict, errRelayOp,
	"outbox relay: command dispatch busy; another worker holds the claim")

// writeBackResults updates entry statuses based on publish outcomes.
// Each Store method call uses its own short transaction with an optimistic
// lock on status='claiming' — this prevents a race where ReclaimStale
// recovers the entry between Phase 2 and Phase 3.
func (r *Relay) writeBackResults(ctx context.Context, results []publishResult) (pollStats, error) {
	var stats pollStats
	for i, res := range results {
		if err := r.writeBackOne(ctx, res, &stats); err != nil {
			remaining := len(results) - i
			slog.Error("outbox relay: writeBack failed mid-batch, remaining entries stay in claiming",
				slog.Int("completed", i),
				slog.Int("remaining", remaining),
				slog.String("entry_id", res.entry.ID()),
				slog.String("event_type", res.entry.EventType()),
				slog.Any("error", err))
			return stats, err
		}
	}
	return stats, nil
}

// writeBackOne settles a single publish result: command idempotency Commit then
// MarkPublished on success, or delegates to handleFailedEntry on failure.
// Extracted to keep writeBackResults below the cognitive-complexity ceiling.
func (r *Relay) writeBackOne(ctx context.Context, res publishResult, stats *pollStats) error {
	if res.err != nil {
		return r.handleFailedEntry(ctx, res, stats)
	}
	// Command dispatch settled successfully — Commit the idempotency lease before
	// publishing the outbox row so the business side effect and dedup done-key do
	// not split. If Commit fails, leave the row in claiming; ReclaimStale can make
	// it retry, but we never mark a command consumed while its done-key was not
	// durably recorded. receipt is nil for events and non-acquired command claims.
	if res.receipt != nil {
		settleCtx, cancel := receiptSettleContext(ctx)
		err := res.receipt.Commit(settleCtx)
		cancel()
		if err != nil {
			slog.Error("outbox relay: command idempotency Commit failed; leaving row un-published",
				slog.String("entry_id", res.entry.ID()),
				slog.Any("error", err))
			return err
		}
	}
	if err := kout.Transition(kout.StateClaiming, kout.StatePublished); err != nil {
		return err
	}
	updated, err := r.store.MarkPublished(ctx, res.entry.ID(), res.entry.LeaseID)
	if err != nil {
		return err
	}
	if !updated {
		// Lease lost — entry was reclaimed (or its row vanished). At-least-
		// once delivery means the broker may already have the message;
		// skip the write-back and let the new lease owner re-issue if needed.
		// Logged at Warn (mirrors the fail-write sibling) so duplicate-publish
		// / lease-race investigations have entry-level evidence, not just a count.
		stats.skipped++
		slog.Warn(
			"outbox relay: stale lease lost write-back",
			slog.String("entry_id", res.entry.ID()),
			slog.String("lease_id", res.entry.LeaseID),
			slog.String("outcome", "published"),
		)
	} else {
		stats.published++
	}
	return nil
}

// handleFailedEntry handles a single failed publish result, updating stats.
// Extracted to keep writeBackResults below cognitive-complexity ceiling.
//
// Mark{Dead,Retry} return updated=false when the lease was lost (ReclaimStale
// already moved the row, or another worker re-claimed). In that case we MUST
// NOT count the failure into stats — the new lease owner will observe the
// canonical outcome — and we emit a Warn so operators can correlate stats
// drift with real reclaim activity. ref: graphile/worker complete_job pattern.
func (r *Relay) handleFailedEntry(ctx context.Context, res publishResult, stats *pollStats) error {
	// Release the command idempotency lease so the entry can be re-claimed on the
	// next attempt (failed dispatch must NOT leave a held lease that would make the
	// retry see ClaimBusy/ClaimDone). receipt is nil for events and for non-acquired
	// command claims. Release failure is logged, not fatal. ref: ConsumerBase
	// settle semantics — Reject/Requeue → Receipt.Release.
	if res.receipt != nil {
		settleCtx, cancel := receiptSettleContext(ctx)
		err := res.receipt.Release(settleCtx)
		cancel()
		if err != nil {
			slog.Warn("outbox relay: command idempotency Release failed",
				slog.String("entry_id", res.entry.ID()),
				slog.Any("error", err))
		}
	}

	newAttempts := res.entry.Attempts + 1
	errMsg := SanitizeError(res.err.Error(), 1000)

	// Permanent failures (deterministic command-dispatch framework errors +
	// MarshalEnvelope failures, tagged via kout.PermanentError) can never recover
	// by retry, so dead-letter them immediately instead of burning the retry
	// budget. Handler business errors are returned unwrapped and fall through to
	// the normal attempt-budget retry path. ref: ADR §Amendment 2026-06-06 r2.
	permanent := isPermanentDispatch(res.err)

	if permanent || newAttempts >= r.cfg.MaxAttempts {
		if err := kout.Transition(kout.StateClaiming, kout.StateDead); err != nil {
			return err
		}
		updated, err := r.store.MarkDead(ctx, res.entry.ID(), res.entry.LeaseID, newAttempts, errMsg)
		if err != nil {
			return err
		}
		if !updated {
			stats.lost++
			slog.Warn(
				"outbox relay: stale lease lost fail-write",
				slog.String("entry_id", res.entry.ID()),
				slog.String("lease_id", res.entry.LeaseID),
				slog.String("outcome", "dead"),
			)
			return nil
		}
		stats.dead++
		logEntryDeadLettered(ctx, res.entry, newAttempts, permanent, errMsg)
		return nil
	}

	// Retry: back to pending with exponential backoff + jitter,
	// preventing thundering herd in multi-relay-instance deployments.
	if err := kout.Transition(kout.StateClaiming, kout.StatePending); err != nil {
		return err
	}
	delay := r.retryDelay(newAttempts)
	nextRetryAt := r.clk().Now().Add(delay)
	updated, err := r.store.MarkRetry(ctx, res.entry.ID(), res.entry.LeaseID, newAttempts, nextRetryAt, errMsg)
	if err != nil {
		return err
	}
	if !updated {
		stats.lost++
		slog.Warn(
			"outbox relay: stale lease lost fail-write",
			slog.String("entry_id", res.entry.ID()),
			slog.String("lease_id", res.entry.LeaseID),
			slog.String("outcome", "retry"),
		)
		return nil
	}
	stats.retried++
	return nil
}

// logEntryDeadLettered emits the dead-letter Error log. Extracted from
// handleFailedEntry to keep it below the cognitive-complexity ceiling. Command
// entries carry their per-instance dedup identity in metadata; surface it so
// command dead-lettering is reconcilable. Non-command (event) entries have no
// command_id — it is omitted then.
func logEntryDeadLettered(ctx context.Context, entry ClaimedEntry, attempts int, permanent bool, errMsg string) {
	attrs := []slog.Attr{
		slog.String("entry_id", entry.ID()),
		slog.String("event_type", entry.EventType()),
		slog.String("aggregate_id", entry.AggregateID()),
		slog.Int("attempts", attempts),
		slog.Bool("permanent", permanent),
		slog.String("last_error", errMsg),
	}
	if cmdID := entry.Metadata()[command.CommandIDMetadataKey]; cmdID != "" {
		attrs = append(attrs, slog.String("command_id", cmdID))
	}
	slog.LogAttrs(ctx, slog.LevelError, "outbox relay: entry dead-lettered", attrs...)
}

// isPermanentDispatch reports whether err is tagged as a permanent (non-retryable)
// failure via kernel/outbox.PermanentError. Generated command DispatchAsync wraps
// its deterministic framework errors (reg nil, routing-topic ≠ DispatchID, payload
// decode failure, no handler, wrong-typed handler) this way, and publishBatch wraps
// MarshalEnvelope failures, so the relay dead-letters them immediately rather than
// retrying to budget exhaustion. Handler business errors are returned unwrapped and
// stay transient (MarkRetry) by default; a handler that knows its failure is
// unrecoverable can itself return a kout.PermanentError to opt in.
func isPermanentDispatch(err error) bool {
	var pe *kout.PermanentError
	return errors.As(err, &pe)
}

// ---------------------------------------------------------------------------
// reclaimStale / cleanup helpers
// ---------------------------------------------------------------------------

// reclaimStale recovers entries stuck in 'claiming' past ClaimTTL.
//
// Each ReclaimStale call caps at cfg.ReclaimBatchSize so the underlying
// UPDATE never produces a multi-second statement that blocks VACUUM /
// replication. We loop until a sweep returns < batchSize, draining a
// large accumulated set promptly inside the same tick rather than waiting up to
// ReclaimInterval per cap-sized chunk. Loop also breaks on ctx cancel.
//
// reclaimMaxIterations bounds the loop so a producer that keeps pushing
// stale claiming rows faster than we can drain them cannot starve the
// reclaim goroutine inside one tick. Hitting the cap indicates accumulated
// stale rows, not a correctness issue — the next reclaim tick will pick
// up where this one left off.
//
// ref: riverqueue/river internal/maintenance/job_rescuer.go (batch loop break)
func (r *Relay) reclaimStale(ctx context.Context) error {
	batch := r.cfg.ReclaimBatchSize
	var total int
	// Defer the metric/log emit so a partial-success sweep (some batches
	// committed, then a later batch errors) still reports the rows already
	// recovered. Reporting only on full success would silently undercount
	// outbox_reclaimed_total during DB blips, exactly the scenario where
	// operators correlate it against outbox_relayed_total{outcome="lost"}.
	defer func() {
		if total > 0 {
			slog.Warn(
				"outbox relay: reclaimed stale entries",
				slog.Int("count", total),
			)
			r.metrics.RecordReclaim(ctx, int64(total))
		}
	}()
	for i := 0; i < reclaimMaxIterations; i++ {
		if err := ctx.Err(); err != nil {
			break
		}
		count, err := r.store.ReclaimStale(
			ctx, r.cfg.ClaimTTL, r.cfg.MaxAttempts,
			r.cfg.BaseRetryDelay, r.cfg.MaxRetryDelay, batch,
		)
		if err != nil {
			return err
		}
		total += count
		if count < batch {
			break
		}
	}
	return nil
}

func receiptSettleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), commandReceiptSettleTimeout)
}

// reclaimMaxIterations is the per-tick safety cap for the reclaim drain
// loop. With the default batch=1000, each tick can recover up to 16,000
// rows; deeper queues spill into the next ReclaimInterval tick. The
// constant is small enough that pathological behavior (Store bug
// returning count==batch forever) cannot block a relay shutdown by
// more than a handful of ReclaimStale round-trips.
const reclaimMaxIterations = 16

// cleanup removes old published and dead entries.
// Loops CleanupPublished / CleanupDead until each returns deleted < batchSize.
//
// NOTE: If an intermediate batch call fails, this function returns early and
// RecordCleanup is not called — already-deleted rows are not counted.
// This is intentionally conservative: under-counting cleanup is safer than
// over-counting.
func (r *Relay) cleanup(ctx context.Context) error {
	const batchLimit = 1000

	publishedCutoff := r.clk().Now().Add(-r.cfg.RetentionPeriod)
	var totalPublished int64
	for {
		deleted, err := r.store.CleanupPublished(ctx, publishedCutoff, batchLimit)
		if err != nil {
			return err
		}
		totalPublished += int64(deleted)
		if deleted < batchLimit {
			break
		}
	}
	if totalPublished > 0 {
		slog.Info(
			"outbox relay: cleaned up published entries",
			slog.Int64("deleted", totalPublished),
		)
	}

	deadCutoff := r.clk().Now().Add(-r.cfg.DeadRetentionPeriod)
	var totalDead int64
	for {
		deleted, err := r.store.CleanupDead(ctx, deadCutoff, batchLimit)
		if err != nil {
			return err
		}
		totalDead += int64(deleted)
		if deleted < batchLimit {
			break
		}
	}
	if totalDead > 0 {
		slog.Info(
			"outbox relay: cleaned up dead entries",
			slog.Int64("deleted", totalDead),
		)
	}

	if totalPublished > 0 || totalDead > 0 {
		r.metrics.RecordCleanup(ctx, totalPublished, totalDead)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backoff helpers
// ---------------------------------------------------------------------------

// cappedDelay caps a duration at MaxRetryDelay.
// ref: adapters/rabbitmq/consumer_base.go cappedDelay
func (r *Relay) cappedDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > r.cfg.MaxRetryDelay {
		return r.cfg.MaxRetryDelay
	}
	return d
}

// ---------------------------------------------------------------------------
// Health and readiness
// ---------------------------------------------------------------------------

// Probes returns the typed health probes contributed by the Relay, one per
// enabled failure budget. Each Probe carries a healthz.ProbeName-typed const
// (ProbePoll / ProbeReclaim / ProbeCleanup) and a Check function with the
// healthz contract: nil return = healthy; non-nil = unhealthy.
//
// Only budgets with a positive threshold are included; threshold=0 (disabled)
// budgets are excluded from the slice so callers can iterate and register
// every returned probe unconditionally.
//
// Consumed by runtime/bootstrap.relayAdapter to satisfy ManagedResource on
// behalf of *Relay (see ADR
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md).
//
// ref: controller-runtime/pkg/healthz AddReadyzCheck — named-checker aggregation.
func (r *Relay) Probes() []healthz.Probe {
	var probes []healthz.Probe
	if r.pollBudget != nil {
		probes = append(probes, healthz.NewProbe(ProbePoll, r.pollBudget.Checker()))
	}
	if r.reclaimBudget != nil {
		probes = append(probes, healthz.NewProbe(ProbeReclaim, r.reclaimBudget.Checker()))
	}
	if r.cleanupBudget != nil {
		probes = append(probes, healthz.NewProbe(ProbeCleanup, r.cleanupBudget.Checker()))
	}
	return probes
}

// Worker returns the Relay itself as the background worker.
// Relay implements kernel/worker.Worker (Start/Stop), so the bootstrap relay
// adapter can manage its goroutine lifecycle.
//
// Consumed by runtime/bootstrap.relayAdapter to satisfy ManagedResource on
// behalf of *Relay.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go — resource self-reports hook.
func (r *Relay) Worker() kworker.Worker {
	return r
}

// Ready returns the channel that is closed when Start() has transitioned the
// relay to the running state. Callers can use this to synchronize without
// polling:
//
//	select {
//	case <-relay.Ready():
//	    // relay is running
//	case <-time.After(deadline):
//	    // timeout
//	}
//
// Ready() never returns nil. Before Start() completes (or after Stop()), the
// returned channel is open and will not close until the next Start() runs to
// completion. Callers that want to detect the "not yet started" state should
// still guard the select with a timeout.
func (r *Relay) Ready() <-chan struct{} {
	r.mu.Lock()
	ch := r.readyCh
	r.mu.Unlock()
	return ch
}

// retryDelayBase is the scaling unit for exponential backoff: delay = base * (retryDelayBase << shift).
const retryDelayBase = 1

// retryJitterDivisor defines the jitter range as a fraction of the delay: jitter ∈ [0, delay/retryJitterDivisor).
const retryJitterDivisor = 4

// retryDelay computes exponential backoff with jitter and cap.
// Formula: cappedDelay(base * 2^attempts) + jitter([0, delay/4])
// ref: adapters/rabbitmq/consumer_base.go claimWithRetry backoff
func (r *Relay) retryDelay(attempts int) time.Duration {
	// Clamp shift exponent to avoid int64 overflow when attempts is unexpectedly
	// large (defensive: real callers stop at MaxAttempts ≤ 10). 1<<30 * 5s already
	// far exceeds MaxRetryDelay so the cap kicks in identically.
	shift := min(attempts, 30)
	delay := r.cappedDelay(r.cfg.BaseRetryDelay * (retryDelayBase << shift))
	if delay > 0 {
		// G404 R2-approved: backoff jitter has no cryptographic requirement.
		// G404 non-crypto retry jitter — no cryptographic requirement.
		jitter := time.Duration(rand.Int64N(int64(delay/retryJitterDivisor) + 1)) //nolint:gosec // see comment above
		delay += jitter
	}
	return delay
}
