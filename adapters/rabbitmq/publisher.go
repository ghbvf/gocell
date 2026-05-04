package rabbitmq

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface check.
var _ outbox.Publisher = (*Publisher)(nil)

// Publisher implements outbox.Publisher using RabbitMQ with confirm mode.
//
// Each Publish call acquires an ephemeral channel (open, use, close per publish),
// consistent with the Watermill defaultChannelProvider pattern. Publisher.Close(ctx)
// waits for all in-flight Publish calls to complete (bounded by ctx deadline).
//
// ref: uber-go/fx app.go StopTimeout — ctx carries shared shutdown budget
// ref: Watermill defaultChannelProvider — ephemeral per-publish channel
type Publisher struct {
	conn   *Connection
	mu     sync.Mutex // guards closed/wg.Add ordering to prevent Add-after-Wait race
	closed atomic.Bool
	wg     sync.WaitGroup
	clock  clock.Clock
}

// PublisherOption configures a Publisher.
type PublisherOption func(*Publisher)

// WithPublisherClock sets the clock used by the Publisher for timeout and
// latency tracking. Required — NewPublisher panics if no clock is supplied.
// Pass clock.Real() at the composition root.
func WithPublisherClock(clk clock.Clock) PublisherOption {
	return func(p *Publisher) {
		p.clock = clk
	}
}

// NewPublisher creates a Publisher backed by the given Connection.
// A clock.Clock must be supplied via WithPublisherClock; NewPublisher panics
// if no clock is provided.
func NewPublisher(conn *Connection, opts ...PublisherOption) *Publisher {
	p := &Publisher{conn: conn}
	for _, o := range opts {
		o(p)
	}
	clock.MustHaveClock(p.clock, "rabbitmq.NewPublisher")
	return p
}

// Close waits for all in-flight Publish calls to complete, bounded by ctx.
// Returns ctx.Err() if ctx is already canceled or if the budget expires while
// waiting for in-flight publishes to complete.
//
// Close is idempotent: a second call returns nil immediately.
//
// ref: uber-go/fx app.go StopTimeout — ctx passes the shared shutdown budget.
func (p *Publisher) Close(ctx context.Context) error {
	// Serialize closed flip with any concurrent Publish's wg.Add to prevent
	// the WaitGroup Add-after-Wait race. After this critical section returns,
	// new Publish calls observe closed==true and bail before Add.
	p.mu.Lock()
	if !p.closed.CompareAndSwap(false, true) {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	return adapterutil.CloseWithDeadline(ctx, "rabbitmq-publisher", func() error {
		p.wg.Wait()
		return nil
	})
}

// Publish sends a message to the given topic (exchange) with publisher confirms.
//
// The topic is used as a fanout exchange name. The exchange is declared
// idempotently on each publish to handle reconnect scenarios.
//
// Returns ErrAdapterAMQPPublish if the publisher has been closed.
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	// Lock-step closed check + wg.Add to guarantee we never Add after Close has
	// flipped closed and started wg.Wait. Without this, sync.WaitGroup would
	// race between Add and Wait under -race.
	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		return errcode.New(errcode.KindInternal, ErrAdapterAMQPPublish, "rabbitmq: publisher is closed")
	}
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()

	ch, err := p.conn.AcquireChannel()
	if err != nil {
		// Preserve terminal error code from Connection so callers can distinguish
		// "permanent config failure" / "reconnect exhausted" from "transient publish failure".
		if isTerminalConnectionError(err) {
			return err
		}
		return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPPublish, "rabbitmq: acquire channel for publish", err)
	}
	// Close the channel after use instead of returning it to the shared pool.
	// Confirm-mode channels pollute the pool: amqp091-go's connection reader
	// blocks on confirms.One() if the registered NotifyPublish listener is
	// full, deadlocking ALL channels on the connection. Watermill uses the
	// same strategy (ephemeral channel per publish) as the default.
	//
	// ref: Watermill defaultChannelProvider — open, use, close per publish.
	defer func() {
		if closeErr := ch.Close(); closeErr != nil {
			slog.Debug("rabbitmq: error closing publish channel",
				slog.Any("error", closeErr))
		}
	}()

	// Declare exchange idempotently.
	if err := ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPPublish, "rabbitmq: declare exchange", err)
	}

	// Enable confirm mode.
	if err := ch.Confirm(false); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPPublish, "rabbitmq: enable confirm mode", err)
	}

	confirmCh := ch.NotifyPublish(make(chan amqp.Confirmation, 1))

	msg := amqp.Publishing{
		ContentType:  "application/octet-stream",
		DeliveryMode: amqp.Persistent,
		Timestamp:    p.clock.Now().UTC(),
		Body:         payload,
	}

	if err := ch.PublishWithContext(ctx, topic, "", false, false, msg); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPPublish, "rabbitmq: publish message", err)
	}

	// Wait for broker confirmation.
	confirmTimer := p.clock.NewTimerAt(p.clock.Now().Add(p.conn.config.ConfirmTimeout))
	defer confirmTimer.Stop()
	select {
	case confirm, ok := <-confirmCh:
		if !ok {
			return errcode.New(errcode.KindInternal, ErrAdapterAMQPConfirmTimeout, "rabbitmq: confirm channel closed")
		}
		if !confirm.Ack {
			return errcode.New(errcode.KindInternal, ErrAdapterAMQPConfirmTimeout, "rabbitmq: broker nacked message")
		}
		slog.Debug("rabbitmq: message published and confirmed",
			slog.String("topic", topic))
		return nil

	case <-confirmTimer.C():
		return errcode.New(errcode.KindInternal, ErrAdapterAMQPConfirmTimeout, "rabbitmq: publish confirm timed out")

	case <-ctx.Done():
		return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPPublish, "rabbitmq: publish context canceled", ctx.Err())
	}
}
