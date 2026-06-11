package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface assertion.
var _ outbox.Publisher = (*Publisher)(nil)

// Publisher implements kernel/outbox.Publisher for MQTT v5 brokers.
//
// Each Publisher is bound to one TopicNamespace and one Connection; Publish
// calls Mint(topic) → conn.Publish, classifies the PUBACK reason code, and
// reports metrics through the injected PublisherCollector.
//
// Concurrency: Publish is safe to call concurrently. Close drains in-flight
// publishes via a WaitGroup but does NOT close the Connection — the
// Connection lifecycle is owned by bootstrap and may be shared across multiple
// publishers in future PRs (e.g., DLT publisher in PR-4).
//
// ref: adapters/rabbitmq/publisher.go closed-guard pattern (Mutex + atomic.Bool + WaitGroup).
type Publisher struct {
	clk       clock.Clock
	conn      *Connection
	ns        TopicNamespace
	collector PublisherCollector

	publishTimeout time.Duration

	mu     sync.Mutex
	closed atomic.Bool
	wg     sync.WaitGroup
}

// PublisherOption configures optional behavior of a Publisher.
type PublisherOption func(*Publisher)

// WithPublisherCollector injects a PublisherCollector that records publish-side
// metrics. Default is NoopPublisherCollector.
func WithPublisherCollector(c PublisherCollector) PublisherOption {
	return func(p *Publisher) {
		if c != nil {
			p.collector = c
		}
	}
}

// NewPublisher constructs a Publisher bound to conn and ns. clk is a required
// positional parameter (CLOCK-POSITIONAL-INJECTION-01). The publish-timeout
// budget comes from conn.cfg.publishTimeout — 0 means no adapter timeout (the
// caller-provided ctx deadline is honored as-is).
//
// Returns error if conn is nil or ns is the zero-value namespace (which would
// fail every Mint).
//
// Signature note: unlike adapters/rabbitmq.NewPublisher (which returns *Publisher
// and panics via clock.MustHaveClock on a nil clock), this constructor returns
// (*Publisher, error). The difference is deliberate: a nil clock is a programmer
// error (panic, same as rabbitmq), but a nil Connection / zero TopicNamespace are
// caller-supplied runtime values validated into a returned error so the bootstrap
// wiring path can surface them as structured diagnostics rather than crashing.
func NewPublisher(clk clock.Clock, conn *Connection, ns TopicNamespace, opts ...PublisherOption) (*Publisher, error) {
	clock.MustHaveClock(clk, "mqtt.NewPublisher")
	if conn == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterMQTTInvalidConfig,
			"mqtt: NewPublisher requires non-nil Connection")
	}
	if ns.String() == "" {
		return nil, errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			"mqtt: NewPublisher requires non-zero TopicNamespace")
	}
	p := &Publisher{
		clk:            clk,
		conn:           conn,
		ns:             ns,
		collector:      NoopPublisherCollector{},
		publishTimeout: conn.cfg.publishTimeout,
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Publish satisfies outbox.Publisher. It validates topic against the publisher's
// TopicNamespace via Mint, derives a child ctx with PublishTimeout if non-zero,
// calls Connection.Publish, and classifies the response.
//
// QoS 1 is used for every publish (caller cannot override).
//
// PublishTimeout: when the configured publish timeout (WithPublishTimeout) > 0, a
// child ctx with that deadline is derived; when 0, the caller-provided ctx is
// honored as-is.
//
// PUBACK 0x10 (NoMatchingSubscribers) is treated as success — counted in
// mqtt_publish_total and ack_duration, with an additional slog.Warn for operator
// visibility. No error is returned.
//
// Records:
//   - PublishFailure{reason} on every failure path
//   - PublishSuccess(ackDuration) on successful PUBACK (including 0x10)
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		p.collector.RecordPublishFailure(ctx, PublishFailureClosed)
		return errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed,
			"mqtt: publisher is closed")
	}
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()

	// Type funnel: Mint enforces PublishOK precedence.
	t, err := p.ns.Mint(topic)
	if err != nil {
		p.collector.RecordPublishFailure(ctx, PublishFailureTopicOutsideNamespace)
		return err
	}

	// Payload size enforcement (per the configured maximum packet size,
	// WithMaximumPacketSize, when non-zero).
	// Compare as int64 to avoid G115 integer overflow: len(payload) fits int64,
	// and MaximumPacketSize is uint32 (max 2^32-1), both representable in int64.
	if max := p.conn.cfg.maximumPacketSize; max > 0 && int64(len(payload)) > int64(max) {
		p.collector.RecordPublishFailure(ctx, PublishFailurePayloadTooLarge)
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTPayloadTooLarge,
			"mqtt: payload exceeds maximum packet size",
			errcode.WithDetails(
				errcode.PublicInt("size", len(payload)),
				errcode.PublicInt("max", int(max)),
			))
	}

	// Derive per-publish timeout if configured.
	publishCtx := ctx
	if p.publishTimeout > 0 {
		var cancel context.CancelFunc
		publishCtx, cancel = context.WithTimeout(ctx, p.publishTimeout)
		defer cancel()
	}

	start := p.clk.Now()
	resp, err := p.conn.Publish(publishCtx, t, payload, publishOpts{QoS: 1, Retain: false})
	if err != nil {
		return p.handlePublishError(ctx, publishCtx, resp, err)
	}

	// Success path. paho returns a nil error ONLY for PUBACK reason codes < 0x80
	// (paho/client.go:923): 0x00 Success and 0x10 NoMatchingSubscribers. Reason
	// codes >= 0x80 (rejected / not-authorized / quota / payload-format) arrive
	// via the error branch above — handlePublishError classifies them. 0x10 is
	// informational: counted as success with an operator warning.
	//
	// topic is the operator's actionable field here; it may carry device/tenant
	// identifiers in IoT deployments — configure slog handler redaction if that
	// is a concern for the target sink.
	if resp != nil && resp.ReasonCode == 0x10 {
		slog.LogAttrs(ctx, slog.LevelWarn, "mqtt: publish succeeded with no matching subscribers",
			slog.String(logKeyClientID, p.conn.cfg.clientID.String()),
			slog.String(logKeyTopic, t.String()))
	}
	p.collector.RecordPublishSuccess(ctx, p.clk.Since(start))
	return nil
}

// handlePublishError classifies the non-nil error returned by Connection.Publish.
// paho multiplexes three distinct failure shapes through this single error, and
// each must map to a different errcode/metric:
//
//  1. Broker-rejected PUBACK (reason >= 0x80): paho returns BOTH a non-nil resp
//     (with resp.ReasonCode set) AND a non-nil error (paho/client.go:923). This
//     is the ONLY path on which the broker's reason code reaches us — it is NOT
//     surfaced via a nil-error resp — so classifyPubackReason MUST be driven from
//     here, not from the success branch.
//  2. Adapter-closed (resp == nil, err is ErrAdapterMQTTClosed): preserve the
//     closed semantics rather than flattening to a generic publish failure.
//  3. Deadline / cancel / transport (resp == nil): classifyPublishErr +
//     wrapPublishErr distinguish caller-ctx cancel, adapter PublishTimeout, and
//     generic transport errors.
func (p *Publisher) handlePublishError(ctx, publishCtx context.Context, resp *paho.PublishResponse, err error) error {
	// (1) Broker-rejected PUBACK reason code (>= 0x80).
	if resp != nil && resp.ReasonCode >= 0x80 {
		code, kind := classifyPubackReason(resp.ReasonCode)
		p.collector.RecordPublishFailure(ctx, pubackReasonToMetric(code))
		return errcode.New(kind, code,
			"mqtt: broker returned non-success PUBACK reason code",
			reasonDetailOptions(int(resp.ReasonCode), pubackReasonName(resp.ReasonCode), isAuthRelatedPubackCode(resp.ReasonCode))...)
	}
	// (2) Adapter closed — preserve the closed errcode rather than wrapping it.
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == ErrAdapterMQTTClosed {
		p.collector.RecordPublishFailure(ctx, PublishFailureClosed)
		return err
	}
	// (3) Deadline / cancel / transport.
	p.collector.RecordPublishFailure(ctx, classifyPublishErr(ctx, publishCtx, err))
	return wrapPublishErr(ctx, publishCtx, err)
}

// Close drains in-flight publishes and marks the publisher closed. It does NOT
// close the underlying Connection (lifecycle owned by bootstrap). Idempotent.
func (p *Publisher) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		return nil
	}
	p.closed.Store(true)
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPublisherCloseTimeout,
			"mqtt: publisher Close timed out waiting for in-flight publishes", ctx.Err())
	}
}

// classifyPublishErr maps a non-PUBACK error into a PublishFailureReason metric
// label, distinguishing the caller's own context from the adapter-derived
// PublishTimeout child context:
//
//   - callerCtx already done (deadline or cancel) → context_canceled: the caller
//     drove the abort, not the adapter's budget.
//   - only the adapter PublishTimeout child fired (callerCtx alive) → puback_timeout.
//   - neither ctx involved → publish_error (transport).
//
// When the configured publish timeout (WithPublishTimeout) == 0, publishCtx ==
// callerCtx, so the first branch owns every deadline/cancel and puback_timeout is
// never (mis)reported.
func classifyPublishErr(callerCtx, publishCtx context.Context, err error) PublishFailureReason {
	if callerCtx.Err() != nil {
		return PublishFailureContextCanceled
	}
	if publishCtx.Err() == context.DeadlineExceeded {
		return PublishFailurePubAckTimeout
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return PublishFailureContextCanceled
	}
	return PublishFailurePublishError
}

// wrapPublishErr wraps a non-PUBACK error into an errcode, mirroring
// classifyPublishErr's caller-vs-adapter-timeout distinction so the returned
// errcode and the recorded metric reason always agree:
//
//   - caller ctx canceled/expired → ErrAdapterMQTTPublishCanceled
//   - adapter PublishTimeout budget exhausted → ErrAdapterMQTTPubAckTimeout
//   - generic transport failure → ErrAdapterMQTTPublishFailed
func wrapPublishErr(callerCtx, publishCtx context.Context, err error) error {
	if callerCtx.Err() != nil {
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPublishCanceled,
			"mqtt: publish canceled by caller context", err)
	}
	if publishCtx.Err() == context.DeadlineExceeded {
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPubAckTimeout,
			"mqtt: PUBACK timeout (adapter PublishTimeout budget exceeded)", err)
	}
	return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPublishFailed,
		"mqtt: publish failed", redactErr(err))
}

// pubackReasonToMetric maps an errcode.Code (from classifyPubackReason) to a
// PublishFailureReason metric label, keeping the errcode↔metric mapping coherent
// (each distinct PUBACK errcode → a distinct, actionable metric reason).
//
// Caller MUST pre-filter ErrAdapterMQTTPublishNoSubscribers — that code is
// treated as success path (0x10 NoMatchingSubscribers) and never reaches this
// mapping. The default branch covers any future drift.
func pubackReasonToMetric(code errcode.Code) PublishFailureReason {
	switch code {
	case ErrAdapterMQTTPublishRateLimited:
		return PublishFailureRateLimited
	case ErrAdapterMQTTPublishNotAuthorized:
		return PublishFailureNotAuthorized
	case ErrAdapterMQTTPublishPayloadFormatInvalid:
		return PublishFailurePayloadFormatInvalid
	case ErrAdapterMQTTPublishRejected:
		return PublishFailureRejected
	default:
		return PublishFailurePublishError
	}
}
