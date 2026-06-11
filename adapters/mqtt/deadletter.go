package mqtt

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/adapters/mqtt/internal/dlxoutcome"
)

// routeDeadLetter publishes a permanently-rejected or undecodable (poison)
// message to the app-level dead-letter sink "$dead/<originalTopic>" for ops
// audit. MQTT has no broker-native dead-letter exchange (unlike AMQP's DLX), so
// this is the adapter's equivalent of rabbitmq's Nack(requeue=false) → DLX.
//
// originalTopic is the topic the message was delivered on:
//   - Reject path: entry.Topic() (the decoded envelope topic);
//   - poison path: pb.Topic (the envelope is undecodable, but the broker-
//     delivered topic is known) — payload is the raw undecodable bytes.
//
// It is called BEFORE the poison ack (ackPoison), so the $dead capture happens
// before redelivery is stopped.
//
// Every exit returns a [dlxoutcome.Outcome]: the metric is recorded by the
// returning constructor, never inline. [dlxoutcome.Dropped] records the alertable
// mqtt_dlx_failed_total; [dlxoutcome.Captured] records mqtt_dlx_total. Because the
// function is typed to return an Outcome and only those two constructors produce
// one, a drop path that forgets the metric cannot compile (gh #1440 / archtest
// MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01). The caller discards the Outcome — its job is
// to force every exit through a recording constructor, not to carry state.
//
// Fail-closed on every error: if the topic cannot be minted (out-of-namespace /
// wildcard poison topic) or the $dead publish fails, routeDeadLetter logs at Error
// and Dropped() increments mqtt_dlx_failed_total (the alertable "dead-letter sink
// unhealthy" signal) WITHOUT recording a successful capture — the caller still
// ack-as-poisons the message so a dead-letter failure can never block intake. This
// fail-closed-and-drop mirrors Kafka Connect's DeadLetterQueueReporter (KIP-298):
// the original is dropped, but the failure is a distinct, alertable metric — NOT
// leave-unacked, which on MQTT would reconnect-redeliver and reintroduce the
// head-of-line stall Option C (ADR-050 §6) avoids. mqtt_dlx_total counts only
// messages actually captured.
func (s *Subscriber) routeDeadLetter(
	ctx context.Context, originalTopic string, payload []byte, reason ConsumeFailureReason,
) dlxoutcome.Outcome { //nolint:unparam // gh #1440: Outcome forces each exit through a recording constructor; callers discard it.
	dlt, err := s.ns.MintDeadLetter(originalTopic)
	if err != nil {
		// safeErrForLog: the mint error (errcode) embeds the untrusted originalTopic
		// in its Internal detail ("topic=<raw>") — sanitize it like the topic field
		// (redactErr would mask secrets but not strip control chars; CWE-117).
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: cannot mint $dead topic; skipping DLT publish (message acked-as-poison)",
			slog.String(logKeyClientID, s.conn.cfg.clientID.String()),
			slog.String(logKeyTopic, safeTopicForLog(originalTopic)),
			slog.String("reason", string(reason)),
			slog.String("error", safeErrForLog(err)))
		return dlxoutcome.Dropped(ctx, s.collector, reason)
	}

	// Bound the publish and use WithoutCancel so the dead-letter capture
	// completes even when the delivery ctx is canceled during graceful shutdown.
	// Reuses SettlementTimeout (a sibling bounded broker op); never 0 (setDefaults).
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.SettlementTimeout)
	defer cancel()
	if _, pubErr := s.conn.Publish(pubCtx, dlt, payload, publishOpts{QoS: 1, Retain: false}); pubErr != nil {
		// redactErr: broker errors may echo connection strings / credentials
		// (key=value), consistent with connection.go / publisher.go logging.
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: dead-letter publish failed; message will be acked-as-poison without DLT capture",
			slog.String(logKeyClientID, s.conn.cfg.clientID.String()),
			slog.String(logKeyDLXTopic, safeTopicForLog(dlt.String())),
			slog.String("reason", string(reason)),
			slog.Any("error", redactErr(pubErr)))
		return dlxoutcome.Dropped(ctx, s.collector, reason)
	}

	slog.LogAttrs(ctx, slog.LevelWarn, "mqtt: routed message to dead-letter sink",
		slog.String(logKeyClientID, s.conn.cfg.clientID.String()),
		slog.String(logKeyTopic, safeTopicForLog(originalTopic)),
		slog.String(logKeyDLXTopic, safeTopicForLog(dlt.String())),
		slog.String("reason", string(reason)))
	return dlxoutcome.Captured(ctx, s.collector, reason)
}
