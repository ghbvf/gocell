package mqtt

import (
	"context"
	"log/slog"
)

// routeDeadLetter publishes a permanently-rejected or undecodable (poison)
// message to the app-level dead-letter sink "$dead/<originalTopic>" for ops
// audit, then records the dead-letter metric. MQTT has no broker-native
// dead-letter exchange (unlike AMQP's DLX), so this is the adapter's equivalent
// of rabbitmq's Nack(requeue=false) → DLX.
//
// originalTopic is the topic the message was delivered on:
//   - Reject path: entry.Topic() (the decoded envelope topic);
//   - poison path: pb.Topic (the envelope is undecodable, but the broker-
//     delivered topic is known) — payload is the raw undecodable bytes.
//
// It is called BEFORE the poison ack (ackPoison), so the $dead capture happens
// before redelivery is stopped.
//
// Fail-closed on every error: if the topic cannot be minted (out-of-namespace /
// wildcard poison topic) or the $dead publish fails, routeDeadLetter logs at
// Error and returns WITHOUT recording a dead-letter — the caller still
// ack-as-poisons the message so a dead-letter failure can never block intake.
// The dead-letter metric counts only messages actually captured in $dead.
func (s *Subscriber) routeDeadLetter(ctx context.Context, originalTopic string, payload []byte, reason ConsumeFailureReason) {
	dlt, err := s.ns.MintDeadLetter(originalTopic)
	if err != nil {
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: cannot mint $dead topic; skipping DLT publish (message acked-as-poison)",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, safeTopicForLog(originalTopic)),
			slog.String("reason", string(reason)),
			slog.Any("error", err))
		return
	}

	// Bound the publish and use WithoutCancel so the dead-letter capture
	// completes even when the delivery ctx is canceled during graceful shutdown.
	// Reuses SettlementTimeout (a sibling bounded broker op); never 0 (setDefaults).
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.SettlementTimeout)
	defer cancel()
	if _, pubErr := s.conn.Publish(pubCtx, dlt, payload, publishOpts{QoS: 1, Retain: false}); pubErr != nil {
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: dead-letter publish failed; message will be acked-as-poison without DLT capture",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyDLXTopic, dlt.String()),
			slog.String("reason", string(reason)),
			slog.Any("error", pubErr))
		return
	}

	s.collector.RecordDeadLetter(ctx, reason)
	slog.LogAttrs(ctx, slog.LevelWarn, "mqtt: routed message to dead-letter sink",
		slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
		slog.String(logKeyDLXTopic, dlt.String()),
		slog.String("reason", string(reason)))
}
