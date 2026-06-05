// Package eventreceive implements the eventreceive slice: the inbound webhook
// receive handler for the webhookdemo example.
//
// Consistency: L0 LocalOnly — the runtime webhook.Receiver verifies the HMAC
// signature, enforces the timestamp window, and claims idempotency BEFORE this
// handler runs. HandleEvent only decodes the already-verified delivery and emits
// a structured log. No transaction, no outbox, no persistence.
package eventreceive

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// eventPayload is the slice-local (DTO scope A) decoded view of the webhook
// body. It mirrors contracts/webhook/demo/events/v1/payload.schema.json; unknown
// fields are ignored (inbound payloads may carry sender extras).
type eventPayload struct {
	EventID string          `json:"eventId"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
}

// Service handles verified inbound webhook deliveries for webhook.demo.events.v1.
type Service struct {
	logger *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the structured logger. A nil logger is ignored; the default
// is slog.Default() (already sealed with the redacting handler by run()).
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// NewService constructs an eventreceive Service. It has no required
// dependencies — the logger falls back to slog.Default().
func NewService(opts ...Option) *Service {
	s := &Service{logger: slog.Default()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// HandleEvent is the webhook.WebhookReceiveHandler for webhook.demo.events.v1
// (signature func(context.Context, webhook.Delivery) error). By the time it runs
// the delivery has passed HMAC verification, the timestamp window check, and the
// idempotency claim in the runtime receiver. This handler decodes the payload and
// logs it; it is idempotent (logging only), satisfying the at-least-once contract.
func (s *Service) HandleEvent(ctx context.Context, d webhook.Delivery) error {
	var evt eventPayload
	if err := json.Unmarshal(d.Payload, &evt); err != nil {
		// The signature already verified, so a body that won't decode is a
		// permanent (non-retryable) failure — return it so the receiver rejects.
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			"eventreceive: decode webhook payload", err)
	}
	s.logger.InfoContext(ctx, "webhookdemo: received verified webhook delivery",
		slog.String("source_id", string(d.SourceID)),
		slog.String("delivery_id", string(d.DeliveryID)),
		slog.String("event_id", evt.EventID),
		slog.String("event_type", evt.Type),
	)
	return nil
}
