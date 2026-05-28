package mqtt

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

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
// budget comes from conn.cfg.PublishTimeout — 0 means no adapter timeout (the
// caller-provided ctx deadline is honored as-is).
//
// Returns error if conn is nil or ns is the zero-value namespace (which would
// fail every Mint).
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
		publishTimeout: conn.cfg.PublishTimeout,
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Publish satisfies outbox.Publisher. It validates topic against the publisher's
// TopicNamespace via Mint, derives a child ctx with PublishTimeout if non-zero,
// calls Connection.Publish (QoS 1 by default), and classifies the response.
//
// Records:
//   - PublishFailure{reason} on every failure path
//   - PublishSuccess(ackDuration) on successful PUBACK
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

	// Payload size enforcement (per Config.MaximumPacketSize when non-zero).
	// Compare as int64 to avoid G115 integer overflow: len(payload) fits int64,
	// and MaximumPacketSize is uint32 (max 2^32-1), both representable in int64.
	if max := p.conn.cfg.MaximumPacketSize; max > 0 && int64(len(payload)) > int64(max) {
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
		reason := classifyPublishErr(publishCtx, err)
		p.collector.RecordPublishFailure(ctx, reason)
		return wrapPublishErr(err)
	}

	// Inspect PUBACK reason code.
	if resp != nil && resp.ReasonCode != 0x00 {
		code, kind := classifyPubackReason(resp.ReasonCode)
		if code != "" && code != ErrAdapterMQTTPublishNoSubscribers {
			// Rejected / RateLimited path — record failure + return wrapped error.
			reason := pubackReasonToMetric(code)
			p.collector.RecordPublishFailure(ctx, reason)
			return errcode.New(kind, code,
				"mqtt: broker returned non-success PUBACK reason code",
				errcode.WithDetails(
					errcode.PublicInt("reasonCode", int(resp.ReasonCode)),
					errcode.PublicString("reasonName", pubackReasonName(resp.ReasonCode)),
				))
		}
		// 0x10 NoMatchingSubscribers is informational: count as success.
	}

	p.collector.RecordPublishSuccess(ctx, p.clk.Since(start))
	return nil
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
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPubAckTimeout,
			"mqtt: publisher Close timed out waiting for in-flight publishes", ctx.Err())
	}
}

// classifyPublishErr maps a transport-level error from autopaho into a
// PublishFailureReason metric label.
func classifyPublishErr(publishCtx context.Context, err error) PublishFailureReason {
	if publishCtx.Err() == context.DeadlineExceeded {
		return PublishFailurePubAckTimeout
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return PublishFailureContextCanceled
	}
	return PublishFailurePublishError
}

// wrapPublishErr wraps an autopaho transport error into an errcode.
// Distinguishes context-cancel/timeout from generic publish failure.
func wrapPublishErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPubAckTimeout,
			"mqtt: PUBACK timeout", err)
	}
	if errors.Is(err, context.Canceled) {
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTConnect,
			"mqtt: publish context canceled", err)
	}
	return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTConnect,
		"mqtt: publish failed", err)
}

// pubackReasonToMetric maps an errcode.Code (from classifyPubackReason) to a
// PublishFailureReason metric label.
func pubackReasonToMetric(code errcode.Code) PublishFailureReason {
	switch code {
	case ErrAdapterMQTTPublishRateLimited:
		return PublishFailureRateLimited
	case ErrAdapterMQTTPayloadTooLarge:
		return PublishFailurePayloadTooLarge
	default:
		return PublishFailurePublishError
	}
}
