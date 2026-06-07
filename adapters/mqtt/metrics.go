package mqtt

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// SubscribeFailureReason classifies why a receive-path SUBSCRIBE failed (initial
// SUBACK rejection or reconnect re-arm failure). The set is closed; callers
// (alerting rules, log queries) can rely on the literals being stable across
// releases. Mirrors PublishFailureReason / ConsumeFailureReason.
type SubscribeFailureReason string

const (
	// subscribeReasonSubackReject means the broker returned a SUBACK reason byte
	// >= 0x80 (e.g. NotAuthorized 0x87, TopicFilterInvalid 0x8F, SharedSubs
	// unsupported 0x9E). The route stays registered so a later reconnect retries.
	subscribeReasonSubackReject SubscribeFailureReason = "suback_reject"
	// subscribeReasonTransport means the SUBSCRIBE wire call itself failed
	// (transport-level error from autopaho, not a SUBACK reason byte).
	subscribeReasonTransport SubscribeFailureReason = "transport"
)

// ConnectionCollector records MQTT connection-level metrics.
// Implementations must be safe for concurrent use.
type ConnectionCollector interface {
	// RecordReconnect increments the reconnect counter for this collector's cell.
	RecordReconnect(ctx context.Context)
	// RecordSubscribeFailure increments the subscribe-failure counter for the
	// given reason. Implementations MUST NOT panic on any reason value; the
	// closed set is enforced by the call site, not the collector.
	RecordSubscribeFailure(ctx context.Context, reason SubscribeFailureReason)
}

// providerConnectionCollector implements ConnectionCollector via a provider-
// neutral metrics.Provider. Wired at the composition root.
//
// Metrics:
//
//	mqtt_reconnect_total        (counter, labels: cell)
//	mqtt_subscribe_failed_total (counter, labels: cell, reason)
//
// ref: adapters/rabbitmq/publisher_metrics.go — same inject-at-construction pattern.
type providerConnectionCollector struct {
	cellID        string
	reconnect     metrics.CounterVec
	subscribeFail metrics.CounterVec
}

var _ ConnectionCollector = (*providerConnectionCollector)(nil)

// errProviderRequired is the error message used when a nil metrics.Provider is
// passed to any of the three NewProvider*Collector constructors.
const errProviderRequired = "mqtt: metrics.Provider is required"

// helpCellLabel is the common suffix appended to metric Help strings to document
// that the "cell" label is bound at construction time (registration-time
// enumerated, not derived from request context).
const helpCellLabel = "Label: cell = construction-time cell identifier (registration-time enumerated, not from request ctx)."

// NewProviderConnectionCollector registers mqtt_reconnect_total and
// mqtt_subscribe_failed_total on p and returns a ConnectionCollector bound to
// cellID. cellID becomes the "cell" label.
//
// Returns error when p is nil, cellID is empty, or the Provider reports
// registration failure (e.g. duplicate metric names). Registration is
// all-or-nothing: a later failure rolls back the metric already registered so
// the provider is not left holding a partial set.
func NewProviderConnectionCollector(p metrics.Provider, cellID string) (ConnectionCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			errProviderRequired)
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: cellID is required for provider connection collector")
	}

	var registered []metrics.Collector
	rollback := func(wrapErr error) error {
		for _, c := range registered {
			_ = p.Unregister(c)
		}
		return wrapErr
	}

	reconnect, err := p.CounterVec(metrics.CounterOpts{
		Name:       "mqtt_reconnect_total",
		Help:       "Total MQTT reconnect events observed by the adapter. " + helpCellLabel,
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register reconnect counter", err))
	}
	registered = append(registered, reconnect)

	subscribeFail, err := p.CounterVec(metrics.CounterOpts{
		Name: "mqtt_subscribe_failed_total",
		Help: "Total number of receive-path SUBSCRIBE failures (initial SUBACK rejection or reconnect re-arm), " +
			"classified by reason. reason ∈ {suback_reject, transport} — closed set; alerting rules can rely on " +
			"the literals. " + helpCellLabel,
		LabelNames: []string{"cell", "reason"},
	})
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register subscribe failed counter", err))
	}
	// subscribeFail is the last registration — nothing after it can fail.

	return &providerConnectionCollector{
		cellID:        cellID,
		reconnect:     reconnect,
		subscribeFail: subscribeFail,
	}, nil
}

// RecordReconnect increments mqtt_reconnect_total{cell=c.cellID}.
func (c *providerConnectionCollector) RecordReconnect(ctx context.Context) {
	c.reconnect.With(metrics.Labels{"cell": c.cellID}).Inc(ctx)
}

// RecordSubscribeFailure increments mqtt_subscribe_failed_total{cell, reason}.
func (c *providerConnectionCollector) RecordSubscribeFailure(ctx context.Context, reason SubscribeFailureReason) {
	c.subscribeFail.With(metrics.Labels{"cell": c.cellID, "reason": string(reason)}).Inc(ctx)
}

// PublishFailureReason classifies why a Publish() call failed. The set is
// closed; callers (alerting rules, log queries) can rely on the literals being
// stable across releases.
type PublishFailureReason string

const (
	// PublishFailurePayloadTooLarge means the message payload exceeded the
	// adapter's configured MaximumPacketSize (client-side guard, pre-network).
	PublishFailurePayloadTooLarge PublishFailureReason = "payload_too_large"
	// PublishFailureClosed means Publish() was called after the adapter was
	// shut down (Close() already called).
	PublishFailureClosed PublishFailureReason = "closed"
	// PublishFailurePubAckTimeout means the broker did not send a PUBACK
	// within the configured deadline (QoS 1 / QoS 2 flows). A publish issued
	// before the connection is established surfaces here (or as
	// context_canceled): autopaho queues the publish until connected, so the
	// caller observes a deadline, not a distinct "never connected" reason.
	PublishFailurePubAckTimeout PublishFailureReason = "puback_timeout"
	// PublishFailureContextCanceled means the caller's context was canceled
	// or its deadline exceeded before the publish completed.
	PublishFailureContextCanceled PublishFailureReason = "context_canceled"
	// PublishFailurePublishError means the underlying MQTT client returned a
	// transport-level error from the publish wire call (not a broker PUBACK
	// reason code).
	PublishFailurePublishError PublishFailureReason = "publish_error"
	// PublishFailureTopicOutsideNamespace means the topic was rejected because
	// it falls outside the adapter's allowed topic namespace.
	PublishFailureTopicOutsideNamespace PublishFailureReason = "topic_outside_namespace"
	// PublishFailureRateLimited means the broker returned PUBACK 0x97 (Quota
	// Exceeded) — the publisher is rate-limited.
	PublishFailureRateLimited PublishFailureReason = "rate_limited"
	// PublishFailureNotAuthorized means the broker returned PUBACK 0x87 (Not
	// Authorized) — the publisher's ACL forbids the topic. Retryable after an
	// operator fixes the broker ACL.
	PublishFailureNotAuthorized PublishFailureReason = "not_authorized"
	// PublishFailurePayloadFormatInvalid means the broker returned PUBACK 0x99
	// (Payload Format Invalid) — the payload violates its declared format/
	// content-type. Distinct from payload_too_large (size).
	PublishFailurePayloadFormatInvalid PublishFailureReason = "payload_format_invalid"
	// PublishFailureRejected means the broker returned a permanent-reject PUBACK
	// reason code (0x80 Unspecified / 0x83 ImplementationSpecific / 0x90
	// TopicNameInvalid) — a client-side fix is required before retrying.
	PublishFailureRejected PublishFailureReason = "rejected"
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
// Metrics (mqtt_ name prefix; metrics.CounterOpts has no Subsystem field):
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
			errProviderRequired)
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: cellID is required for provider publisher collector")
	}

	// Register the three metrics with all-or-nothing semantics: if a later
	// registration fails (e.g. a duplicate metric name), roll back the ones
	// already registered so the provider is not left holding a partial set.
	var registered []metrics.Collector
	rollback := func(wrapErr error) error {
		for _, c := range registered {
			_ = p.Unregister(c)
		}
		return wrapErr
	}

	publishTotal, err := p.CounterVec(metrics.CounterOpts{
		Name: "mqtt_publish_total",
		Help: "Total number of MQTT publish attempts that completed successfully (broker PUBACK received). " +
			helpCellLabel,
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register publish total counter", err))
	}
	registered = append(registered, publishTotal)

	publishFailed, err := p.CounterVec(metrics.CounterOpts{
		Name: "mqtt_publish_failed_total",
		Help: "Total number of MQTT publish attempts that failed, classified by reason. " +
			"reason ∈ {payload_too_large, closed, puback_timeout, context_canceled, publish_error, " +
			"topic_outside_namespace, rate_limited, not_authorized, payload_format_invalid, rejected} — " +
			"closed set; alerting rules can rely on the literals. " + helpCellLabel,
		LabelNames: []string{"cell", "reason"},
	})
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register publish failed counter", err))
	}
	registered = append(registered, publishFailed)

	ackDuration, err := p.HistogramVec(metrics.HistogramOpts{
		Name: "mqtt_publish_ack_duration_seconds",
		Help: "End-to-end MQTT publish ack round-trip duration in seconds, from Publish() call to broker PUBACK. " +
			"Buckets cover 1 ms to 10 s; values outside this range fall into the +Inf bucket. " + helpCellLabel,
		LabelNames: []string{"cell"},
		Buckets:    ackDurationBuckets,
	})
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register publish ack duration histogram", err))
	}
	// ackDuration is the last registration — nothing after it can fail, so it
	// need not be appended to the rollback set.

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

// ---------------------------------------------------------------------------
// Subscriber metrics (PR-3)
// ---------------------------------------------------------------------------

// ConsumeFailureReason classifies why a received MQTT message did not result in
// a successful Ack. The set is closed; callers (alerting rules, log queries) can
// rely on the literals being stable across releases. Mirrors PublishFailureReason.
type ConsumeFailureReason string

const (
	// consumeReasonUnmarshal means the received PUBLISH payload could not be
	// decoded into the v1 outbox wire envelope (poison message). The message is
	// routed to $dead/<topic> then acked-as-consumed so it cannot block intake
	// forever (see deadletter.go routeDeadLetter).
	consumeReasonUnmarshal ConsumeFailureReason = "unmarshal"
	// consumeReasonReject means the handler returned DispositionReject (permanent
	// failure). The message is routed to $dead/<topic> then acked-as-poison (see
	// deadletter.go routeDeadLetter).
	consumeReasonReject ConsumeFailureReason = "reject"
	// consumeReasonRequeue means the handler returned DispositionRequeue (transient
	// failure). The message is left unacked so the broker redelivers on session
	// resume / reconnect.
	consumeReasonRequeue ConsumeFailureReason = "requeue"
	// consumeReasonCommitFailed means Settlement.Commit failed on the Ack path
	// (lease expired / idempotency backend error). The message is left unacked so
	// another holder retries.
	consumeReasonCommitFailed ConsumeFailureReason = "commit_failed"
	// consumeReasonAckFailed means Settlement.Commit SUCCEEDED but the subsequent
	// broker ack failed. This is the dangerous redeliver case: the idempotency key
	// (ClaimDone) is already committed, so the redelivered message is guarded
	// against reprocessing, but the broker will redeliver until the ack lands.
	// Distinct from commit_failed — the commit half succeeded, only the ack half
	// failed — so alerting can tell apart "lease/idempotency backend trouble"
	// (commit_failed) from "broker ack path trouble" (ack_failed).
	consumeReasonAckFailed ConsumeFailureReason = "ack_failed"
	// consumeReasonUnknownDisposition means the handler returned a zero / invalid
	// Disposition. Treated as Requeue (left unacked) defensively.
	consumeReasonUnknownDisposition ConsumeFailureReason = "unknown_disposition"
)

// SubscriberCollector observes subscriber-side metrics. Construction-time cellID
// becomes the "cell" label value (registration-time enumerated set, NOT derived
// from request context — per observability.md HTTP metrics cell-label convention).
//
// Implementations MUST bind the "cell" label at construction time, NOT from
// request ctx. Implementations must be safe for concurrent use.
type SubscriberCollector interface {
	// RecordConsumeSuccess increments the success counter and observes the
	// end-to-end handler+commit duration for a successfully acked message.
	RecordConsumeSuccess(ctx context.Context, dur time.Duration)
	// RecordConsumeFailure increments the failure counter for the given reason.
	// Implementations MUST NOT panic on any reason value; the closed set is
	// enforced by the call site, not the collector.
	RecordConsumeFailure(ctx context.Context, reason ConsumeFailureReason)
	// RecordDeadLetter increments the dead-letter counter for a message routed to
	// the app-level $dead/<topic> sink. reason ∈ {unmarshal, reject} (poison or
	// permanent failure). Implementations MUST NOT panic on any reason value.
	RecordDeadLetter(ctx context.Context, reason ConsumeFailureReason)
	// RecordDeadLetterFailure increments the dead-letter-FAILURE counter when the
	// $dead/<topic> publish itself fails (topic unmintable or broker publish
	// error) and the message is consequently acked-as-poison WITHOUT being
	// captured in $dead. This is a distinct, higher-severity operational signal
	// from RecordConsumeFailure ("a message failed processing") — it means the
	// dead-letter sink itself is unhealthy and messages are being dropped.
	// Operators SHOULD alert on it. reason ∈ {unmarshal, reject}.
	// ref: Kafka Connect KIP-298 deadletterqueue-produce-failures (distinct from
	// total-record-errors).
	RecordDeadLetterFailure(ctx context.Context, reason ConsumeFailureReason)
	// AdjustInflight adds delta to the in-flight consume gauge
	// mqtt_consume_inflight{cell} — +1 when a delivery enters processing, -1 when
	// it completes (ack) or is dropped (intake stopped). The gauge reflects the
	// count of deliveries currently being processed; the cell label is bound at
	// construction time (no caller-supplied label, so it cannot be forged).
	AdjustInflight(ctx context.Context, delta int64)
}

// NoopSubscriberCollector is the default collector used when no observability is
// wired. Method bodies intentionally empty — registration cost is zero and
// metric absence is documented behavior, not a fault.
type NoopSubscriberCollector struct{}

// RecordConsumeSuccess is a no-op.
func (NoopSubscriberCollector) RecordConsumeSuccess(_ context.Context, _ time.Duration) { /* no-op */ }

// RecordConsumeFailure is a no-op.
func (NoopSubscriberCollector) RecordConsumeFailure(_ context.Context, _ ConsumeFailureReason) { /* no-op */
}

// RecordDeadLetter is a no-op.
func (NoopSubscriberCollector) RecordDeadLetter(_ context.Context, _ ConsumeFailureReason) { /* no-op */
}

// RecordDeadLetterFailure is a no-op.
func (NoopSubscriberCollector) RecordDeadLetterFailure(_ context.Context, _ ConsumeFailureReason) { /* no-op */
}

// AdjustInflight is a no-op.
func (NoopSubscriberCollector) AdjustInflight(_ context.Context, _ int64) { /* no-op */ }

// Compile-time interface check.
var _ SubscriberCollector = NoopSubscriberCollector{}

// providerSubscriberCollector implements SubscriberCollector via a provider-
// neutral metrics.Provider. Wired at the composition root.
//
// Metrics (mqtt_ name prefix; metrics.CounterOpts has no Subsystem field):
//
//	mqtt_consume_total              (counter,   labels: cell)
//	mqtt_consume_failed_total       (counter,   labels: cell, reason)
//	mqtt_dlx_total                  (counter,   labels: cell, reason)
//	mqtt_dlx_failed_total           (counter,   labels: cell, reason)
//	mqtt_consume_duration_seconds   (histogram, labels: cell; buckets 1ms–10s)
//	mqtt_consume_inflight           (gauge,     labels: cell)
//
// ref: adapters/mqtt/metrics.go providerPublisherCollector — same inject-at-
// construction + all-or-nothing registration pattern.
type providerSubscriberCollector struct {
	cellID          string
	consumeTotal    metrics.CounterVec
	consumeFailed   metrics.CounterVec
	dlxTotal        metrics.CounterVec
	dlxFailed       metrics.CounterVec
	consumeDur      metrics.HistogramVec
	consumeInflight metrics.GaugeVec
}

var _ SubscriberCollector = (*providerSubscriberCollector)(nil)

// consumeDurationBuckets covers MQTT consume handler+commit times from 1 ms to
// 10 s, reusing the publish ack bucket choice (network-latency-oriented set).
var consumeDurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Subscriber metric definitions (data; registered in registration order by
// NewProviderSubscriberCollector). Extracted to package level to keep the
// constructor's registration loop short.
var (
	subConsumeTotalOpts = metrics.CounterOpts{
		Name: "mqtt_consume_total",
		Help: "Total number of MQTT messages consumed successfully (handler Ack + Settlement Commit). " +
			helpCellLabel,
		LabelNames: []string{"cell"},
	}
	subConsumeFailedOpts = metrics.CounterOpts{
		Name: "mqtt_consume_failed_total",
		Help: "Total number of MQTT messages that did not result in a successful Ack, classified by reason. " +
			"reason ∈ {unmarshal, reject, requeue, commit_failed, ack_failed, unknown_disposition} — closed set; " +
			"alerting rules can rely on the literals. " + helpCellLabel,
		LabelNames: []string{"cell", "reason"},
	}
	subDlxTotalOpts = metrics.CounterOpts{
		Name: "mqtt_dlx_total",
		Help: "Total number of messages routed to the app-level dead-letter sink $dead/<topic>. " +
			"reason ∈ {unmarshal, reject} — closed set (poison / permanent failure). " +
			helpCellLabel,
		LabelNames: []string{"cell", "reason"},
	}
	subDlxFailedOpts = metrics.CounterOpts{
		Name: "mqtt_dlx_failed_total",
		Help: "Total number of messages whose dead-letter publish to $dead/<topic> FAILED (topic " +
			"unmintable or broker publish error); the message was acked-as-poison WITHOUT $dead capture. " +
			"A distinct, higher-severity signal from mqtt_consume_failed_total: it means the dead-letter " +
			"sink itself is unhealthy. reason ∈ {unmarshal, reject} — closed set. " + helpCellLabel,
		LabelNames: []string{"cell", "reason"},
	}
	subConsumeDurOpts = metrics.HistogramOpts{
		Name: "mqtt_consume_duration_seconds",
		Help: "End-to-end MQTT consume duration in seconds, from handler invocation to Settlement Commit. " +
			"Buckets cover 1 ms to 10 s; values outside this range fall into the +Inf bucket. " +
			helpCellLabel,
		LabelNames: []string{"cell"},
		Buckets:    consumeDurationBuckets,
	}
	subConsumeInflightOpts = metrics.GaugeOpts{
		Name: "mqtt_consume_inflight",
		Help: "Number of MQTT messages in-flight in the subscriber, counted from receive-callback entry to " +
			"ack/drop. At saturation this is MaxConcurrentHandlers active handlers plus at most one delivery " +
			"awaiting a worker slot (the single-goroutine receive handoff), so it may briefly exceed " +
			"MaxConcurrentHandlers by one. Maintained by AdjustInflight(+1/-1). " + helpCellLabel,
		LabelNames: []string{"cell"},
	}
)

// NewProviderSubscriberCollector registers 6 metrics on p (4 counters +
// 1 histogram + the mqtt_consume_inflight gauge) and returns a
// SubscriberCollector bound to cellID. cellID becomes the "cell" label value.
//
// Returns an error when p is nil, cellID is empty, or the Provider reports a
// registration failure (e.g. duplicate metric names). Registration is
// all-or-nothing: a later failure rolls back the metrics already registered so
// the provider is not left holding a partial set.
func NewProviderSubscriberCollector(p metrics.Provider, cellID string) (SubscriberCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			errProviderRequired)
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: cellID is required for provider subscriber collector")
	}

	var registered []metrics.Collector
	rollback := func(wrapErr error) error {
		for _, c := range registered {
			_ = p.Unregister(c)
		}
		return wrapErr
	}

	consumeTotal, cErr := p.CounterVec(subConsumeTotalOpts)
	if cErr != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register consume total counter", cErr))
	}
	registered = append(registered, consumeTotal)

	consumeFailed, cErr := p.CounterVec(subConsumeFailedOpts)
	if cErr != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register consume failed counter", cErr))
	}
	registered = append(registered, consumeFailed)

	dlxTotal, cErr := p.CounterVec(subDlxTotalOpts)
	if cErr != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register dlx counter", cErr))
	}
	registered = append(registered, dlxTotal)

	dlxFailed, cErr := p.CounterVec(subDlxFailedOpts)
	if cErr != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register dlx failed counter", cErr))
	}
	registered = append(registered, dlxFailed)

	consumeDur, err := p.HistogramVec(subConsumeDurOpts)
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register consume duration histogram", err))
	}
	registered = append(registered, consumeDur)

	consumeInflight, err := p.GaugeVec(subConsumeInflightOpts)
	if err != nil {
		return nil, rollback(errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register consume inflight gauge", err))
	}
	// consumeInflight is the last registration — nothing after it can fail, so it
	// need not be appended to the rollback set.

	return &providerSubscriberCollector{
		cellID:          cellID,
		consumeTotal:    consumeTotal,
		consumeFailed:   consumeFailed,
		dlxTotal:        dlxTotal,
		dlxFailed:       dlxFailed,
		consumeDur:      consumeDur,
		consumeInflight: consumeInflight,
	}, nil
}

// RecordConsumeSuccess increments mqtt_consume_total{cell} and observes
// mqtt_consume_duration_seconds{cell} = dur.Seconds().
func (c *providerSubscriberCollector) RecordConsumeSuccess(ctx context.Context, dur time.Duration) {
	c.consumeTotal.With(metrics.Labels{"cell": c.cellID}).Inc(ctx)
	c.consumeDur.With(metrics.Labels{"cell": c.cellID}).Observe(ctx, dur.Seconds())
}

// RecordConsumeFailure increments mqtt_consume_failed_total{cell, reason}.
// mqtt_consume_total is NOT incremented — failure is not counted as success.
func (c *providerSubscriberCollector) RecordConsumeFailure(ctx context.Context, reason ConsumeFailureReason) {
	c.consumeFailed.With(metrics.Labels{"cell": c.cellID, "reason": string(reason)}).Inc(ctx)
}

// RecordDeadLetter increments mqtt_dlx_total{cell, reason} when a message is
// routed to the app-level $dead/<topic> sink (poison or permanent reject).
func (c *providerSubscriberCollector) RecordDeadLetter(ctx context.Context, reason ConsumeFailureReason) {
	c.dlxTotal.With(metrics.Labels{"cell": c.cellID, "reason": string(reason)}).Inc(ctx)
}

// RecordDeadLetterFailure increments mqtt_dlx_failed_total{cell, reason} when the
// $dead/<topic> publish fails and the message is dropped (acked-as-poison) without
// DLT capture — the alertable "dead-letter sink unhealthy" signal.
func (c *providerSubscriberCollector) RecordDeadLetterFailure(ctx context.Context, reason ConsumeFailureReason) {
	c.dlxFailed.With(metrics.Labels{"cell": c.cellID, "reason": string(reason)}).Inc(ctx)
}

// AdjustInflight adds delta to mqtt_consume_inflight{cell}. Add (not Set) keeps
// the gauge race-free under concurrent deliveries: deltas commute, so the gauge
// converges to the true count whatever the interleaving.
func (c *providerSubscriberCollector) AdjustInflight(ctx context.Context, delta int64) {
	c.consumeInflight.With(metrics.Labels{"cell": c.cellID}).Add(ctx, float64(delta))
}
