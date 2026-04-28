package metrics

import (
	"context"
	"errors"
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConfigEventOutcome is the low-cardinality result taxonomy for config event
// consumer processing.
type ConfigEventOutcome string

const (
	ConfigEventOutcomeAck            ConfigEventOutcome = "ack"
	ConfigEventOutcomeStale          ConfigEventOutcome = "stale"
	ConfigEventOutcomePermanentError ConfigEventOutcome = "permanent_error"
	ConfigEventOutcomeReject         ConfigEventOutcome = "reject"
)

// ConfigEventCollector records config event consumer outcomes.
type ConfigEventCollector interface {
	RecordEventProcessed(cellID, sliceID string, outcome ConfigEventOutcome)
}

// NoopConfigEventCollector drops config event observations.
type NoopConfigEventCollector struct{}

func (NoopConfigEventCollector) RecordEventProcessed(string, string, ConfigEventOutcome) {
	// Intentionally empty: callers can inject this collector when config-event
	// metrics are disabled while keeping service code free of nil checks.
}

type providerConfigEventCollector struct {
	processed kernelmetrics.CounterVec
}

var _ ConfigEventCollector = (*providerConfigEventCollector)(nil)

// NewProviderConfigEventCollector registers config-event consumer metrics on p.
// The Prometheus provider namespace supplies the "gocell_" fqName prefix.
func NewProviderConfigEventCollector(p kernelmetrics.Provider) (ConfigEventCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: config event Provider is required")
	}
	processed, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "config_event_processed_total",
		Help:       "Total number of config events processed by consumers, partitioned by outcome.",
		LabelNames: []string{"cell", "slice", "outcome"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register config_event_processed_total: %w", err)
	}
	return &providerConfigEventCollector{processed: processed}, nil
}

func (c *providerConfigEventCollector) RecordEventProcessed(cellID, sliceID string, outcome ConfigEventOutcome) {
	if c == nil {
		return
	}
	c.processed.With(kernelmetrics.Labels{
		"cell":    cellID,
		"slice":   sliceID,
		"outcome": string(outcome),
	}).Inc()
}

// ConfigEventSubscription identifies a config-event subscription whose final
// non-permanent reject should be counted by ConfigEventRejectMiddleware.
type ConfigEventSubscription struct {
	CellID        string
	SliceID       string
	Topic         string
	ConsumerGroup string
}

// ConfigEventRejectMiddleware records outcome=reject only after downstream
// middleware returns a final non-permanent Reject. Register it outside
// ConsumerBase so retry exhaustion is counted once, after all local attempts.
func ConfigEventRejectMiddleware(
	collector ConfigEventCollector,
	targets ...ConfigEventSubscription,
) outbox.SubscriptionMiddleware {
	if collector == nil {
		collector = NoopConfigEventCollector{}
	}
	targetBySub := make(map[string]ConfigEventSubscription, len(targets))
	for _, target := range targets {
		targetBySub[configEventSubscriptionKey(target.Topic, target.ConsumerGroup)] = target
	}
	return func(sub outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		target, ok := targetBySub[configEventSubscriptionKey(sub.Topic, sub.ConsumerGroup)]
		if !ok {
			return next
		}
		return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			result := next(ctx, entry)
			if result.Disposition != outbox.DispositionReject || isPermanentConfigEventReject(result.Err) {
				return result
			}
			collector.RecordEventProcessed(target.CellID, target.SliceID, ConfigEventOutcomeReject)
			return result
		}
	}
}

func configEventSubscriptionKey(topic, consumerGroup string) string {
	return topic + "\x00" + consumerGroup
}

func isPermanentConfigEventReject(err error) bool {
	if err == nil {
		return false
	}
	var permErr *outbox.PermanentError
	return errors.As(err, &permErr)
}
