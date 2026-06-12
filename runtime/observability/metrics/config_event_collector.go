package metrics

import (
	"context"
	"fmt"
	"strings"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConfigEventProcessReason is the low-cardinality handler/process taxonomy for
// config event consumers. It is intentionally separate from broker settlement.
type ConfigEventProcessReason string

const (
	ConfigEventProcessReasonAck            ConfigEventProcessReason = "ack"
	ConfigEventProcessReasonStale          ConfigEventProcessReason = "stale"
	ConfigEventProcessReasonPermanentError ConfigEventProcessReason = "permanent_error"
	// ConfigEventProcessReasonNoTenant is recorded when tenant.FromContext returns
	// an error (no tenant in consumer context). The event is Rejected to the DLQ
	// (fail-closed): an event reaching the consumer without a tenant envelope is a
	// pipeline-integrity violation, not a retriable condition — retrying cannot
	// supply a tenant that was absent at delivery time.
	ConfigEventProcessReasonNoTenant ConfigEventProcessReason = "no_tenant"
	// ConfigEventProcessReasonTransient is recorded when the config getter
	// returns a transient (non-permanent, non-404) error so the entry is
	// Requeued for retry.
	ConfigEventProcessReasonTransient ConfigEventProcessReason = "transient"
)

// ConfigEventCollector records config event process and settlement metrics.
type ConfigEventCollector interface {
	RecordEventProcess(ctx context.Context, cellID, sliceID string, reason ConfigEventProcessReason)
	RecordEventSettlement(ctx context.Context, cellID, sliceID, disposition string, result outbox.SettlementResult)
}

// NoopConfigEventCollector drops config event observations.
type NoopConfigEventCollector struct{}

func (NoopConfigEventCollector) RecordEventProcess(_ context.Context, _, _ string, _ ConfigEventProcessReason) {
	// Intentionally empty: callers can inject this collector when config-event
	// metrics are disabled while keeping service code free of nil checks.
}

func (NoopConfigEventCollector) RecordEventSettlement(_ context.Context, _, _, _ string, _ outbox.SettlementResult) {
	// Intentionally empty: callers can inject this collector when config-event
	// metrics are disabled while keeping service code free of nil checks.
}

type providerConfigEventCollector struct {
	process    kernelmetrics.CounterVec
	settlement kernelmetrics.CounterVec
}

var _ ConfigEventCollector = (*providerConfigEventCollector)(nil)

// NewProviderConfigEventCollector registers config-event consumer metrics on p.
// The Prometheus provider namespace supplies the "gocell_" fqName prefix.
func NewProviderConfigEventCollector(p kernelmetrics.Provider) (ConfigEventCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: config event Provider is required")
	}
	process, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "config_event_process_total",
		Help:       "Total number of config event handler process results, partitioned by reason.",
		LabelNames: []string{"cell", "slice", "reason"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register config_event_process_total: %w", err)
	}
	settlement, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "config_event_settlement_total",
		Help:       "Total number of config event delivery settlements, partitioned by disposition and result.",
		LabelNames: []string{"cell", "slice", "disposition", "result"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register config_event_settlement_total: %w", err)
	}
	return &providerConfigEventCollector{process: process, settlement: settlement}, nil
}

func (c *providerConfigEventCollector) RecordEventProcess(ctx context.Context, cellID, sliceID string, reason ConfigEventProcessReason) {
	if c == nil {
		return
	}
	c.process.With(kernelmetrics.Labels{
		"cell":   cellID,
		"slice":  sliceID,
		"reason": string(reason),
	}).Inc(ctx)
}

func (c *providerConfigEventCollector) RecordEventSettlement(
	ctx context.Context, cellID, sliceID, disposition string, result outbox.SettlementResult,
) {
	if c == nil {
		return
	}
	c.settlement.With(kernelmetrics.Labels{
		"cell":        cellID,
		"slice":       sliceID,
		"disposition": disposition,
		"result":      string(result),
	}).Inc(ctx)
}

type configEventOwner struct {
	cellID  string
	sliceID string
}

type configEventOwnerContextKey struct{}

// RecordConfigEventProcess records a handler/process reason using the owner
// metadata installed by ConfigEventMiddleware.
func RecordConfigEventProcess(ctx context.Context, collector ConfigEventCollector, reason ConfigEventProcessReason) {
	if collector == nil {
		collector = NoopConfigEventCollector{}
	}
	owner, ok := ctx.Value(configEventOwnerContextKey{}).(configEventOwner)
	if !ok || owner.cellID == "" || owner.sliceID == "" {
		return
	}
	collector.RecordEventProcess(ctx, owner.cellID, owner.sliceID, reason)
}

// ConfigEventMiddleware installs config-event owner metadata into the handler
// context so that RecordConfigEventProcess can retrieve it inside the
// EntryHandler. Settlement observation is handled at the SubscriberHandler
// layer by WrapConfigEventSubscriber; this middleware only injects owner ctx.
//
// The non-config-prefix fast path (isConfigEventSubscription == false) is
// legitimate: non-config subscriptions and audit/command topics pass through
// without instrumentation. Config subscriptions missing owner metadata are
// intercepted at registration time by ConfigEventOwnerValidator and never
// reach this middleware.
func ConfigEventMiddleware() outbox.SubscriptionMiddleware {
	return func(sub outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		if !isConfigEventSubscription(sub) {
			// Fast path: non-config-prefix or non-config topic — skip instrumentation.
			return next
		}
		owner := configEventOwner{cellID: sub.ObservabilityID(), sliceID: sub.SliceID}
		return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			ctx = context.WithValue(ctx, configEventOwnerContextKey{}, owner)
			return next(ctx, entry)
		}
	}
}

// WrapConfigEventSubscriber wraps a SubscriberHandler to append a settlement
// observer that records final broker disposition via ConfigEventCollector. This
// is the SubscriberHandler-layer counterpart to ConfigEventMiddleware (which
// handles the EntryHandler layer).
//
// Non-config subscriptions take the fast path and return next unchanged.
// ConfigEventCollector nil is replaced with NoopConfigEventCollector.
func WrapConfigEventSubscriber(
	collector ConfigEventCollector, sub outbox.Subscription, next outbox.SubscriberHandler,
) outbox.SubscriberHandler {
	if collector == nil {
		collector = NoopConfigEventCollector{}
	}
	if !isConfigEventSubscription(sub) {
		return next
	}
	owner := configEventOwner{cellID: sub.ObservabilityID(), sliceID: sub.SliceID}
	return func(ctx context.Context, entry outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		out, settlement := next(ctx, entry)
		out.SettlementObservers = append(out.SettlementObservers, configEventSettlementObserver{
			collector: collector,
			owner:     owner,
		})
		return out, settlement
	}
}

// ConfigEventOwnerValidator enforces that any subscription on an event.config.*
// topic carries owner metadata (CellID + SliceID) so config-event observability
// cannot be silently dropped at runtime. Composition roots register this with
// EventRouter.AddSubscriptionValidator.
//
// ref: kratos middleware/recovery — fail at registration boundary, not at
// delivery time, so misconfigurations surface during bootstrap.
func ConfigEventOwnerValidator(sub outbox.Subscription) error {
	if !strings.HasPrefix(sub.Topic, "event.config.") {
		return nil
	}
	if sub.CellID == "" || sub.SliceID == "" {
		return fmt.Errorf("config-event subscription %q requires CellID and SliceID owner metadata (use cell.WithSubscriptionSliceID)", sub.Topic)
	}
	return nil
}

// isConfigEventSubscription returns true when sub has a config-event topic prefix
// and both CellID and SliceID owner fields set. Non-config topics are the fast
// path (legitimate skip); config topics missing owner are intercepted at
// registration time by ConfigEventOwnerValidator — they will not reach this
// function at delivery time.
func isConfigEventSubscription(sub outbox.Subscription) bool {
	return sub.CellID != "" &&
		sub.SliceID != "" &&
		strings.HasPrefix(sub.Topic, "event.config.")
}

type configEventSettlementObserver struct {
	collector ConfigEventCollector
	owner     configEventOwner
}

func (o configEventSettlementObserver) ObserveSettlement(ctx context.Context, obs outbox.SettlementObservation) {
	o.collector.RecordEventSettlement(ctx, o.owner.cellID, o.owner.sliceID, obs.Disposition.String(), obs.Result)
}
