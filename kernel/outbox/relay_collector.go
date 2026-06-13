package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// DefaultRelayPollBuckets are histogram buckets for poll phase duration (5ms–10s).
// Matches the range the original adapters/prometheus impl used before
// migration; preserved here so Grafana dashboards continue to look natural.
var DefaultRelayPollBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// DefaultRelayBatchBuckets are histogram buckets for batch size (1–500).
var DefaultRelayBatchBuckets = []float64{1, 5, 10, 25, 50, 100, 200, 500}

// entryKind is the sealed value set for the `kind` label on outbox_relayed_total:
// the NATURE of a settled outbox row — "event" (marshaled + published to the broker)
// vs "command" (decoded + dispatched to an in-process handler under the two-phase
// Claimer). Orthogonal to relayOutcome (the disposition). The value set is single-
// sourced by these consts: metricschema statically resolves them into the
// metrics-schema golden (Hard byte-lock) and OUTBOX-RELAY-LABEL-VALUES-FROZEN-01's
// callsite guard bans inline-constant args to recordOutcome (so only these consts can
// reach the label). #1674.
type entryKind string

const (
	kindEvent   entryKind = "event"
	kindCommand entryKind = "command"
)

// relayOutcome is the sealed value set for the `outcome` label on
// outbox_relayed_total: the DISPOSITION a settled row reached. Orthogonal to
// entryKind. Single-sourced + frozen by the same mechanism as entryKind (golden Hard
// byte-lock + recordOutcome callsite guard). There is deliberately NO "dispatched"
// value: command-vs-event is the orthogonal `kind` label, not an outcome (#1674).
type relayOutcome string

const (
	outcomePublished relayOutcome = "published"
	outcomeRetried   relayOutcome = "retried"
	outcomeDead      relayOutcome = "dead"
	outcomeSkipped   relayOutcome = "skipped"
	outcomeLost      relayOutcome = "lost"
)

// RelayedHelp is the help text for outbox_relayed_total. It is the SINGLE source of
// the help string: tools/metricschema statically resolves this exported const into
// the metrics-schema golden (the static scanner reads it from the type graph, exactly
// as it resolves the entryKind/relayOutcome value sets and the bucket consts), so the
// golden's help and the Prometheus-registered help cannot diverge — there is no
// hand-copied literal to drift. Exported so the schema scan and schema_test can bind
// to it by name.
const RelayedHelp = "Total number of outbox entries processed by the relay, by entry kind and outcome. " +
	"kind=event is broker publish; kind=command is in-process async command dispatch. " +
	"outcome=published|retried|dead are canonical writebacks; " +
	"outcome=skipped covers MarkPublished updated=false (success path lost lease) " +
	"and outcome=lost covers Mark{Retry,Dead} updated=false (failure path lost lease) — " +
	"the canonical outcome for both is owned by the reclaimer (see outbox_reclaimed_total)."

// providerRelayCollector implements RelayCollector via a provider-neutral
// metrics.Provider. Callers supply the Provider at wire time (prom in prod,
// OTel in future deployments, Nop in tests); the collector itself has no
// backend knowledge.
//
// Metrics (subsystem=outbox):
//
//	outbox_relayed_total         (counter, labels: cell, kind, outcome)
//	outbox_poll_duration_seconds (histogram, labels: cell, phase)
//	outbox_batch_size            (histogram, labels: cell)
//	outbox_reclaimed_total       (counter, labels: cell)
//	outbox_cleaned_total         (counter, labels: cell, status)
//
// ref: Temporal MetricsHandler — inject-at-construction pattern
// ref: Watermill components/metrics — publish_time_seconds semantics
type providerRelayCollector struct {
	cellID       string
	relayed      metrics.CounterVec
	pollDuration metrics.HistogramVec
	batchSize    metrics.HistogramVec
	reclaimed    metrics.CounterVec
	cleaned      metrics.CounterVec
}

var _ RelayCollector = (*providerRelayCollector)(nil)

// ProviderRelayCollectorConfig customizes metric naming / bucketing.
// Zero value is acceptable and produces defaults.
type ProviderRelayCollectorConfig struct {
	// PollBuckets overrides DefaultRelayPollBuckets; zero value uses defaults.
	PollBuckets []float64
	// BatchBuckets overrides DefaultRelayBatchBuckets; zero value uses defaults.
	BatchBuckets []float64
}

// NewProviderRelayCollector registers outbox relay metrics on p and returns
// a RelayCollector that records through them. Returns error when cellID is
// empty or when the Provider reports registration failure (typically
// duplicate metric names).
func NewProviderRelayCollector(p metrics.Provider, cellID string, opts ...ProviderRelayCollectorConfig) (RelayCollector, error) {
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"outbox: cellID is required for provider relay collector")
	}
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid, "outbox: metrics.Provider is required")
	}
	cfg := ProviderRelayCollectorConfig{}
	if len(opts) > 0 {
		cfg = opts[0]
	}
	if len(cfg.PollBuckets) == 0 {
		cfg.PollBuckets = DefaultRelayPollBuckets
	}
	if len(cfg.BatchBuckets) == 0 {
		cfg.BatchBuckets = DefaultRelayBatchBuckets
	}

	col, err := registerRelayMetrics(p, cellID, cfg)
	if err != nil {
		return nil, err
	}
	return col, nil
}

// registerRelayMetrics registers all outbox relay metrics on p and returns the
// fully-built collector. On any partial failure the already-registered metrics
// are unregistered in LIFO order so the Provider is left clean.
func registerRelayMetrics(p metrics.Provider, cellID string, cfg ProviderRelayCollectorConfig) (*providerRelayCollector, error) {
	// registered tracks successfully registered collectors in order. On any
	// partial failure the rollback function unregisters them in LIFO order so
	// the Provider is left in a clean state, allowing the caller to retry
	// construction without "duplicate collector" errors.
	var registered []metrics.Collector
	rollback := func(origErr error) error {
		for _, v := range slices.Backward(registered) {
			if rbErr := p.Unregister(v); rbErr != nil {
				slog.Error("outbox: unregister during rollback failed",
					slog.Any("error", rbErr),
					slog.String("cell", cellID),
				)
			}
		}
		return origErr
	}

	// register is the single mechanism through which collectors enter the
	// `registered` slice. Each metric registration funnels through it so the
	// "register + check err + append" pattern cannot drift out of sync —
	// missing the append step is structurally impossible (previously a latent
	// rollback gap when a future metric was added after `cleaned`).
	register := func(c metrics.Collector, err error, name string) error {
		if err != nil {
			return rollback(fmt.Errorf("outbox: register %s: %w", name, err))
		}
		registered = append(registered, c)
		return nil
	}

	relayed, err := p.CounterVec(metrics.CounterOpts{
		Name:       "outbox_relayed_total",
		Help:       RelayedHelp,
		LabelNames: []string{"cell", "kind", "outcome"},
	})
	if err := register(relayed, err, "outbox_relayed_total"); err != nil {
		return nil, err
	}

	pollDuration, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "outbox_poll_duration_seconds",
		Help:       "Duration of each relay poll phase in seconds.",
		LabelNames: []string{"cell", "phase"},
		Buckets:    cfg.PollBuckets,
	})
	if err := register(pollDuration, err, "outbox_poll_duration_seconds"); err != nil {
		return nil, err
	}

	batchSize, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "outbox_batch_size",
		Help:       "Number of entries claimed per relay poll cycle.",
		LabelNames: []string{"cell"},
		Buckets:    cfg.BatchBuckets,
	})
	if err := register(batchSize, err, "outbox_batch_size"); err != nil {
		return nil, err
	}

	reclaimed, err := p.CounterVec(metrics.CounterOpts{
		Name:       "outbox_reclaimed_total",
		Help:       "Total number of stale entries reclaimed by the relay.",
		LabelNames: []string{"cell"},
	})
	if err := register(reclaimed, err, "outbox_reclaimed_total"); err != nil {
		return nil, err
	}

	cleaned, err := p.CounterVec(metrics.CounterOpts{
		Name:       "outbox_cleaned_total",
		Help:       "Total number of entries cleaned up (deleted) by the relay.",
		LabelNames: []string{"cell", "status"},
	})
	if err := register(cleaned, err, "outbox_cleaned_total"); err != nil {
		return nil, err
	}

	return &providerRelayCollector{
		cellID:       cellID,
		relayed:      relayed,
		pollDuration: pollDuration,
		batchSize:    batchSize,
		reclaimed:    reclaimed,
		cleaned:      cleaned,
	}, nil
}

// recordOutcome emits one outbox_relayed_total increment for a non-zero count of
// entries that settled to (kind, outcome). It is the SOLE emission point for the
// kind/outcome labels: the kind and outcome params are the sealed entryKind /
// relayOutcome types, and OUTBOX-RELAY-LABEL-VALUES-FROZEN-01's callsite guard bans
// inline-constant args, so only the declared consts can reach the label.
//
// Zero (and negative) counts are skipped to keep time-series cardinality clean — a
// persistent zero fragment would otherwise appear in Grafana for a {kind,outcome} a
// cell never produces. Concretely, an event-only relay passes r.Command =
// OutcomeCounts{} (all-zero), so every recordOutcome(kindCommand, …) call returns
// early and the {kind="command",…} series are NEVER created (operators must tolerate
// "no data" for command series on event-only cells; use `... or vector(0)` in PromQL).
// This is INTENTIONALLY asymmetric with RecordBatchSize, which always observes
// (including size=0) so dashboards can detect a totally idle relay — batch size is a
// liveness gauge, whereas a zero outcome is the absence of that outcome, not a tick.
func (c *providerRelayCollector) recordOutcome(ctx context.Context, kind entryKind, outcome relayOutcome, n int) {
	if n <= 0 {
		return
	}
	c.relayed.With(metrics.Labels{
		"cell":    c.cellID,
		"kind":    string(kind),
		"outcome": string(outcome),
	}).Add(ctx, float64(n))
}

// RecordPollCycle emits one relayed_total increment per non-zero (kind, outcome)
// and four poll_duration observations (claim, publish, write_back, total). Event and
// command settlements land on the same five outcomes under distinct kind labels, so
// command in-process dispatch throughput/failures are reconcilable separately from
// event broker publish (#1674).
func (c *providerRelayCollector) RecordPollCycle(ctx context.Context, r PollCycleResult) {
	for _, kc := range []struct {
		kind   entryKind
		counts OutcomeCounts
	}{
		{kindEvent, r.Event},
		{kindCommand, r.Command},
	} {
		c.recordOutcome(ctx, kc.kind, outcomePublished, kc.counts.Published)
		c.recordOutcome(ctx, kc.kind, outcomeRetried, kc.counts.Retried)
		c.recordOutcome(ctx, kc.kind, outcomeDead, kc.counts.Dead)
		c.recordOutcome(ctx, kc.kind, outcomeSkipped, kc.counts.Skipped)
		c.recordOutcome(ctx, kc.kind, outcomeLost, kc.counts.Lost)
	}

	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "claim"}).Observe(ctx, r.ClaimDur.Seconds())
	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "publish"}).Observe(ctx, r.PublishDur.Seconds())
	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "write_back"}).Observe(ctx, r.WriteBackDur.Seconds())
	total := (r.ClaimDur + r.PublishDur + r.WriteBackDur).Seconds()
	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "total"}).Observe(ctx, total)
}

// RecordBatchSize observes the claim count of each poll, including zero to
// capture idle cycles (useful for relay liveness panels).
func (c *providerRelayCollector) RecordBatchSize(ctx context.Context, size int) {
	c.batchSize.With(metrics.Labels{"cell": c.cellID}).Observe(ctx, float64(size))
}

// RecordReclaim emits only when count > 0; dropping zero avoids a noisy
// counter increment every cleanup interval on a healthy relay.
func (c *providerRelayCollector) RecordReclaim(ctx context.Context, count int64) {
	if count > 0 {
		c.reclaimed.With(metrics.Labels{"cell": c.cellID}).Add(ctx, float64(count))
	}
}

// RecordCleanup splits increments by status so dashboards can track
// published-vs-dead cleanup separately.
func (c *providerRelayCollector) RecordCleanup(ctx context.Context, publishedDeleted, deadDeleted int64) {
	if publishedDeleted > 0 {
		c.cleaned.With(metrics.Labels{"cell": c.cellID, "status": "published"}).Add(ctx, float64(publishedDeleted))
	}
	if deadDeleted > 0 {
		c.cleaned.With(metrics.Labels{"cell": c.cellID, "status": "dead"}).Add(ctx, float64(deadDeleted))
	}
}
