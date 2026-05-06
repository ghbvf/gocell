package rabbitmq

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
func (NoopPublisherCollector) RecordPublishFailure(_ PublishFailureReason) { /* no-op: metrics disabled */ }

// Compile-time interface check.
var _ PublisherCollector = NoopPublisherCollector{}
