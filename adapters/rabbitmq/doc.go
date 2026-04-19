// Package rabbitmq provides a RabbitMQ adapter for the GoCell event bus.
//
// It implements outbox.Publisher and outbox.Subscriber using amqp091-go,
// with auto-reconnect (exponential backoff), subscriber channel pooling,
// publisher confirm mode (ephemeral channel per publish), and consumer-side
// ConsumerBase (idempotency + retry + DLQ).
//
// # Publisher lifecycle
//
// Publisher uses a fresh channel per Publish call (open, confirm, publish,
// close) to avoid confirm-mode channels polluting the shared pool. For
// high-throughput scenarios, a dedicated long-lived confirm channel is a
// future optimization (see Watermill pooledChannelProvider).
//
// Publisher.Close(ctx context.Context) error waits for all in-flight Publish
// calls to complete, bounded by ctx (uber-go/fx StopTimeout semantics).
//
// # Subscriber lifecycle and A19 reconnect fix
//
// Each subscribeOnce invocation is encapsulated in a subscriptionRun struct
// (ch + consumerTag + localWg). This replaces the previous three-table design
// (s.channels / s.consumerTags / shared s.wg) and fixes A19:
//
//	Previous: reconnect path called closeChannelOnReconnect(ch) concurrently
//	with processDelivery goroutines still referencing ch → ErrClosed noise.
//
//	Fixed: subscribeOnce calls run.waitAndClose(ctx) after consumeLoop exits,
//	guaranteeing all processDelivery goroutines finish before ch.Close().
//	sync.Once guard prevents double-close when subscribeOnce exit path and
//	Subscriber.Close() sweep the same run concurrently.
//
// Subscriber.Close(ctx context.Context) error waits for the global WaitGroup
// (all subscribeOnce goroutines), bounded by ctx. Remaining runs are then
// swept via waitAndClose for final channel cleanup.
//
// # Connection lifecycle
//
// Connection.Close(ctx context.Context) error signals the reconnect loop
// (via closeCh), drains the channel pool, then closes the underlying AMQP
// connection in a goroutine so that the caller's ctx budget is honoured even
// if the broker connection.close frame exchange blocks.
// Connection implements lifecycle.ContextCloser.
//
// ref: ThreeDotsLabs/watermill-amqp — reconnect + ACK/NACK + channel lifecycle
// ref: uber-go/fx app.go StopTimeout — ctx as shared shutdown budget
// ref: rabbitmq/amqp091-go channel.go — Cancel→drain→Wait→Close graceful stop order
// ref: nats-io/nats.go — per-subscription state encapsulation (subscriptionRun)
package rabbitmq
