package mqtt

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConnectionCollector records MQTT connection-level metrics.
// Implementations must be safe for concurrent use.
type ConnectionCollector interface {
	// RecordReconnect increments the reconnect counter for this collector's cell.
	RecordReconnect(ctx context.Context)
}

// providerConnectionCollector implements ConnectionCollector via a provider-
// neutral metrics.Provider. Wired at the composition root.
//
// Metric:
//
//	mqtt_reconnect_total (counter, labels: cell)
//
// ref: adapters/rabbitmq/publisher_metrics.go — same inject-at-construction pattern.
type providerConnectionCollector struct {
	cellID    string
	reconnect metrics.CounterVec
}

var _ ConnectionCollector = (*providerConnectionCollector)(nil)

// NewProviderConnectionCollector registers mqtt_reconnect_total on p and
// returns a ConnectionCollector bound to cellID. cellID becomes the "cell" label.
//
// Returns error when p is nil, cellID is empty, or the Provider reports
// registration failure (e.g. duplicate metric names).
func NewProviderConnectionCollector(p metrics.Provider, cellID string) (ConnectionCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: metrics.Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: cellID is required for provider connection collector")
	}
	reconnect, err := p.CounterVec(metrics.CounterOpts{
		Name:       "mqtt_reconnect_total",
		Help:       "Total MQTT reconnect events observed by the adapter, by cell.",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register reconnect counter", err)
	}
	return &providerConnectionCollector{cellID: cellID, reconnect: reconnect}, nil
}

// RecordReconnect increments mqtt_reconnect_total{cell=c.cellID}.
func (c *providerConnectionCollector) RecordReconnect(ctx context.Context) {
	c.reconnect.With(metrics.Labels{"cell": c.cellID}).Inc(ctx)
}

// PublishFailureReason classifies why a Publish() call failed. The set is
// closed; callers (alerting rules, log queries) can rely on the literals being
// stable across releases.
type PublishFailureReason string

const (
	// PublishFailurePayloadTooLarge means the message payload exceeded the
	// broker's or adapter's configured maximum size.
	PublishFailurePayloadTooLarge PublishFailureReason = "payload_too_large"
	// PublishFailureNeverConnected means Publish() was called before the
	// adapter established its first successful connection.
	PublishFailureNeverConnected PublishFailureReason = "never_connected"
	// PublishFailureClosed means Publish() was called after the adapter was
	// shut down (Close() already called).
	PublishFailureClosed PublishFailureReason = "closed"
	// PublishFailurePubAckTimeout means the broker did not send a PUBACK
	// within the configured deadline (QoS 1 / QoS 2 flows).
	PublishFailurePubAckTimeout PublishFailureReason = "puback_timeout"
	// PublishFailureContextCanceled means the caller's context was canceled
	// or its deadline exceeded before the publish completed.
	PublishFailureContextCanceled PublishFailureReason = "context_canceled"
	// PublishFailurePublishError means the underlying MQTT client returned
	// an error from the publish wire call.
	PublishFailurePublishError PublishFailureReason = "publish_error"
	// PublishFailureTopicOutsideNamespace means the topic was rejected because
	// it falls outside the adapter's allowed topic namespace.
	PublishFailureTopicOutsideNamespace PublishFailureReason = "topic_outside_namespace"
	// PublishFailureRateLimited means the publish attempt was rejected by the
	// adapter's rate limiter before the message reached the broker.
	PublishFailureRateLimited PublishFailureReason = "rate_limited"
)

// PublisherCollector observes publisher-side metrics. Construction-time cellID
// becomes the "cell" label value (registration-time enumerated set, NOT derived
// from request context — per observability.md HTTP metrics cell-label convention).
//
// Implementations MUST bind the "cell" label at construction time, NOT
// from request ctx — per observability.md §HTTP Metrics cell Label
// (cell = registration-time enumerated owner dimension).
//
// Implementations must be safe for concurrent use.
type PublisherCollector interface {
	// RecordPublishSuccess increments the success counter and observes the
	// end-to-end ack round-trip duration for a completed publish.
	RecordPublishSuccess(ctx context.Context, ackDuration time.Duration)
	// RecordPublishFailure increments the failure counter for the given reason.
	// Implementations MUST NOT panic on any reason value; the closed set is
	// enforced by the call site, not the collector.
	RecordPublishFailure(ctx context.Context, reason PublishFailureReason)
}

// NoopPublisherCollector is the default collector used when no observability
// is wired. Method bodies intentionally empty — registration cost is zero and
// metric absence is documented behavior, not a fault.
type NoopPublisherCollector struct{}

// RecordPublishSuccess is a no-op.
func (NoopPublisherCollector) RecordPublishSuccess(_ context.Context, _ time.Duration) { /* no-op */ }

// RecordPublishFailure is a no-op.
func (NoopPublisherCollector) RecordPublishFailure(_ context.Context, _ PublishFailureReason) { /* no-op */
}

// Compile-time interface checks.
var _ PublisherCollector = NoopPublisherCollector{}

// providerPublisherCollector implements PublisherCollector via a provider-
// neutral metrics.Provider. Wired at the composition root.
//
// Metrics (subsystem=mqtt):
//
//	mqtt_publish_total                 (counter,   labels: cell)
//	mqtt_publish_failed_total          (counter,   labels: cell, reason)
//	mqtt_publish_ack_duration_seconds  (histogram, labels: cell; buckets 1ms–10s)
//
// ref: adapters/rabbitmq/publisher_metrics.go — same inject-at-construction
// pattern, extended with success counter and ack duration histogram (AC-8).
type providerPublisherCollector struct {
	cellID        string
	publishTotal  metrics.CounterVec
	publishFailed metrics.CounterVec
	ackDuration   metrics.HistogramVec
}

var _ PublisherCollector = (*providerPublisherCollector)(nil)

// ackDurationBuckets covers MQTT publish ack round-trip times from 1 ms to
// 10 s, matching a standard Prometheus default-ish bucket set oriented to
// network-latency distributions.
var ackDurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// NewProviderPublisherCollector registers 3 metrics on p and returns a
// PublisherCollector bound to cellID. cellID becomes the "cell" label value.
//
// Returns an error when p is nil, cellID is empty, or the Provider reports a
// registration failure (e.g. duplicate metric names).
func NewProviderPublisherCollector(p metrics.Provider, cellID string) (PublisherCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: metrics.Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: cellID is required for provider publisher collector")
	}

	publishTotal, err := p.CounterVec(metrics.CounterOpts{
		Name: "mqtt_publish_total",
		Help: "Total number of MQTT publish attempts that completed successfully (broker PUBACK received). " +
			"Label: cell = construction-time cell identifier (registration-time enumerated, not from request ctx).",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register publish total counter", err)
	}

	publishFailed, err := p.CounterVec(metrics.CounterOpts{
		Name: "mqtt_publish_failed_total",
		Help: "Total number of MQTT publish attempts that failed, classified by reason. " +
			"reason ∈ {payload_too_large, never_connected, closed, puback_timeout, context_canceled, " +
			"publish_error, topic_outside_namespace, rate_limited} — closed set; alerting rules can rely " +
			"on the literals. Label: cell = construction-time cell identifier.",
		LabelNames: []string{"cell", "reason"},
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register publish failed counter", err)
	}

	ackDuration, err := p.HistogramVec(metrics.HistogramOpts{
		Name: "mqtt_publish_ack_duration_seconds",
		Help: "End-to-end MQTT publish ack round-trip duration in seconds, from Publish() call to broker PUBACK. " +
			"Buckets cover 1 ms to 10 s; values outside this range fall into the +Inf bucket. " +
			"Label: cell = construction-time cell identifier.",
		LabelNames: []string{"cell"},
		Buckets:    ackDurationBuckets,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register publish ack duration histogram", err)
	}

	return &providerPublisherCollector{
		cellID:        cellID,
		publishTotal:  publishTotal,
		publishFailed: publishFailed,
		ackDuration:   ackDuration,
	}, nil
}

// RecordPublishSuccess increments mqtt_publish_total{cell} and observes
// mqtt_publish_ack_duration_seconds{cell} = ackDuration.Seconds().
func (c *providerPublisherCollector) RecordPublishSuccess(ctx context.Context, ackDuration time.Duration) {
	c.publishTotal.With(metrics.Labels{"cell": c.cellID}).Inc(ctx)
	c.ackDuration.With(metrics.Labels{"cell": c.cellID}).Observe(ctx, ackDuration.Seconds())
}

// RecordPublishFailure increments mqtt_publish_failed_total{cell, reason}.
// mqtt_publish_total is NOT incremented — failure is not counted as success.
func (c *providerPublisherCollector) RecordPublishFailure(ctx context.Context, reason PublishFailureReason) {
	c.publishFailed.With(metrics.Labels{"cell": c.cellID, "reason": string(reason)}).Inc(ctx)
}
