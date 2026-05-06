package rabbitmq

import (
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// PublishFailureReason classifies why a Publish() call did not complete the
// confirm round-trip. Values form a closed set; callers (collectors,
// alerting rules, log queries) can rely on the literals being stable.
type PublishFailureReason string

const (
	// PublishFailureNack means the broker explicitly NACKed the message.
	// Likely cause: queue full, mandatory unroutable, broker resource alarm.
	PublishFailureNack PublishFailureReason = "nack"
	// PublishFailureTimeout means the confirm timer fired before the broker
	// returned an Ack/Nack. Likely cause: network partition, broker overload.
	PublishFailureTimeout PublishFailureReason = "timeout"
	// PublishFailureChanClosed means the confirm channel closed before the
	// broker returned a Confirmation. Likely cause: connection or channel
	// lost mid-flight.
	PublishFailureChanClosed PublishFailureReason = "chan_closed"
)

// PublisherCollector observes RabbitMQ publish-side failures with enough
// classification (reason) for alerting rules to distinguish broker rejection,
// network timeout, and connection loss without parsing log strings.
//
// The collector is injected at composition root via WithPublisherCollector;
// the default is NoopPublisherCollector so adapter callers can wire RMQ
// without observability without paying registration cost.
//
// Implementations must be safe for concurrent use.
//
// ref: kernel/outbox.RelayCollector — same inject-at-construction pattern.
type PublisherCollector interface {
	// RecordPublishFailure increments the failure counter for the given reason.
	// Implementations MUST NOT panic on any reason value; the closed set is
	// enforced by the call site, not the collector.
	RecordPublishFailure(reason PublishFailureReason)
}

// NoopPublisherCollector is the default collector used when no observability
// is wired. Method bodies intentionally empty — registration cost is zero
// and metric absence is documented behavior, not a fault.
type NoopPublisherCollector struct{}

// RecordPublishFailure is a no-op.
func (NoopPublisherCollector) RecordPublishFailure(_ PublishFailureReason) { /* no-op: metrics disabled */
}

// Compile-time interface check.
var _ PublisherCollector = NoopPublisherCollector{}

// providerPublisherCollector implements PublisherCollector via a provider-
// neutral metrics.Provider. Wired at the composition root with a real Prom
// provider in production and metricsmock in tests.
//
// Metric (subsystem=rabbitmq):
//
//	rabbitmq_publish_failed_total (counter, labels: cell, reason)
//
// reason ∈ {nack, timeout, chan_closed} — closed set defined in
// PublishFailureReason; alerting rules can rely on the literals.
//
// ref: kernel/outbox.providerRelayCollector — same inject-at-construction
// pattern, same provider-neutral surface.
type providerPublisherCollector struct {
	cellID string
	failed metrics.CounterVec
}

var _ PublisherCollector = (*providerPublisherCollector)(nil)

// NewProviderPublisherCollector registers the rabbitmq_publish_failed_total
// counter on p and returns a PublisherCollector backed by it.
// Returns error when cellID is empty or when the Provider reports registration
// failure (typically duplicate metric names).
func NewProviderPublisherCollector(p metrics.Provider, cellID string) (PublisherCollector, error) {
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"rabbitmq: cellID is required for provider publisher collector")
	}
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"rabbitmq: metrics.Provider is required")
	}
	failed, err := p.CounterVec(metrics.CounterOpts{
		Name: "rabbitmq_publish_failed_total",
		Help: "Total number of RabbitMQ publish attempts that failed at the wire " +
			"level, by reason. reason=nack covers broker NACK; " +
			"reason=timeout covers confirm-timer fired before broker reply; " +
			"reason=chan_closed covers confirm channel closed mid-flight.",
		LabelNames: []string{"cell", "reason"},
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"rabbitmq: register publish failed counter", err)
	}
	return &providerPublisherCollector{cellID: cellID, failed: failed}, nil
}

// RecordPublishFailure increments rabbitmq_publish_failed_total{cell, reason}.
func (c *providerPublisherCollector) RecordPublishFailure(reason PublishFailureReason) {
	c.failed.With(metrics.Labels{"cell": c.cellID, "reason": string(reason)}).Inc()
}
