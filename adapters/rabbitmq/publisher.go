package rabbitmq

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

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
	closed atomic.Bool
	wg     sync.WaitGroup
}

// NewPublisher creates a Publisher backed by the given Connection.
func NewPublisher(conn *Connection) *Publisher {
	return &Publisher{conn: conn}
}

// Close waits for all in-flight Publish calls to complete, bounded by ctx.
// Returns ctx.Err() if ctx is already cancelled or if the budget expires while
// waiting for in-flight publishes to complete.
//
// Close is idempotent: a second call returns nil immediately.
//
// ref: uber-go/fx app.go StopTimeout — ctx passes the shared shutdown budget.
func (p *Publisher) Close(ctx context.Context) error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("rabbitmq: publisher closed gracefully")
		return nil
	case <-ctx.Done():
		slog.Warn("rabbitmq: publisher close budget exceeded",
			slog.Any("error", ctx.Err()))
		return ctx.Err()
	}
}

// Publish sends a message to the given topic (exchange) with publisher confirms.
//
// The topic is used as a fanout exchange name. The exchange is declared
// idempotently on each publish to handle reconnect scenarios.
//
// Returns ErrAdapterAMQPPublish if the publisher has been closed.
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if p.closed.Load() {
		return errcode.New(ErrAdapterAMQPPublish, "rabbitmq: publisher is closed")
	}
	p.wg.Add(1)
	defer p.wg.Done()

	ch, err := p.conn.AcquireChannel()
	if err != nil {
		// Preserve terminal error code from Connection so callers can distinguish
		// "permanent config failure" / "reconnect exhausted" from "transient publish failure".
		if isTerminalConnectionError(err) {
			return err
		}
		return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: acquire channel for publish", err)
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
				slog.String("error", closeErr.Error()))
		}
	}()

	// Declare exchange idempotently.
	if err := ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil); err != nil {
		return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: declare exchange", err)
	}

	// Enable confirm mode.
	if err := ch.Confirm(false); err != nil {
		return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: enable confirm mode", err)
	}

	confirmCh := ch.NotifyPublish(make(chan amqp.Confirmation, 1))

	msg := amqp.Publishing{
		ContentType:  "application/octet-stream",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now().UTC(),
		Body:         payload,
	}

	if err := ch.PublishWithContext(ctx, topic, "", false, false, msg); err != nil {
		return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: publish message", err)
	}

	// Wait for broker confirmation.
	select {
	case confirm, ok := <-confirmCh:
		if !ok {
			return errcode.New(ErrAdapterAMQPConfirmTimeout, "rabbitmq: confirm channel closed")
		}
		if !confirm.Ack {
			return errcode.New(ErrAdapterAMQPConfirmTimeout, "rabbitmq: broker nacked message")
		}
		slog.Debug("rabbitmq: message published and confirmed",
			slog.String("topic", topic))
		return nil

	case <-time.After(p.conn.config.ConfirmTimeout):
		return errcode.New(ErrAdapterAMQPConfirmTimeout, "rabbitmq: publish confirm timed out")

	case <-ctx.Done():
		return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: publish context cancelled", ctx.Err())
	}
}
