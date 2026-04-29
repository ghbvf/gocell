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
)

// ConfigEventCollector records config event process and settlement metrics.
type ConfigEventCollector interface {
	RecordEventProcess(cellID, sliceID string, reason ConfigEventProcessReason)
	RecordEventSettlement(cellID, sliceID, disposition string, result outbox.SettlementResult)
}

// NoopConfigEventCollector drops config event observations.
type NoopConfigEventCollector struct{}

func (NoopConfigEventCollector) RecordEventProcess(string, string, ConfigEventProcessReason) {
	// Intentionally empty: callers can inject this collector when config-event
	// metrics are disabled while keeping service code free of nil checks.
}

func (NoopConfigEventCollector) RecordEventSettlement(string, string, string, outbox.SettlementResult) {
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
		return nil, errcode.New(errcode.ErrObservabilityConfigInvalid,
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

func (c *providerConfigEventCollector) RecordEventProcess(cellID, sliceID string, reason ConfigEventProcessReason) {
	if c == nil {
		return
	}
	c.process.With(kernelmetrics.Labels{
		"cell":   cellID,
		"slice":  sliceID,
		"reason": string(reason),
	}).Inc()
}

func (c *providerConfigEventCollector) RecordEventSettlement(cellID, sliceID, disposition string, result outbox.SettlementResult) {
	if c == nil {
		return
	}
	c.settlement.With(kernelmetrics.Labels{
		"cell":        cellID,
		"slice":       sliceID,
		"disposition": disposition,
		"result":      string(result),
	}).Inc()
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
	collector.RecordEventProcess(owner.cellID, owner.sliceID, reason)
}

// ConfigEventMiddleware installs config-event owner metadata for process
// metrics and appends a settlement observer for final broker disposition.
// The non-config-prefix fast path (isConfigEventSubscription == false) is
// legitimate: non-config subscriptions and audit/command topics pass through
// without instrumentation. Config subscriptions missing owner metadata are
// intercepted at registration time by ConfigEventOwnerValidator and never
// reach this middleware.
func ConfigEventMiddleware(collector ConfigEventCollector) outbox.SubscriptionMiddleware {
	if collector == nil {
		collector = NoopConfigEventCollector{}
	}
	return func(sub outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		if !isConfigEventSubscription(sub) {
			// Fast path: non-config-prefix or non-config topic — skip instrumentation.
			return next
		}
		owner := configEventOwner{cellID: sub.CellID, sliceID: sub.SliceID}
		return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			ctx = context.WithValue(ctx, configEventOwnerContextKey{}, owner)
			result := next(ctx, entry)
			result.SettlementObservers = append(result.SettlementObservers, configEventSettlementObserver{
				collector: collector,
				owner:     owner,
			})
			return result
		}
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

func (o configEventSettlementObserver) ObserveSettlement(_ context.Context, obs outbox.SettlementObservation) {
	o.collector.RecordEventSettlement(o.owner.cellID, o.owner.sliceID, obs.Disposition.String(), obs.Result)
}
