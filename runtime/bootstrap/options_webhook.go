package bootstrap

// options_webhook.go — With* option functions for inbound-webhook receiver wiring.
//
// Covers: WithWebhookSourceStore, WithWebhookClaimer.
//
// Both are "cumulative builder" options (see runtime-api.md §Option 范式分层):
// nil inputs are silently ignored; the final nil check happens inside
// phase5DrainWebhookReceivers when a cell has registered receivers.

import (
	"github.com/ghbvf/gocell/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/validation"
)

// WithWebhookSourceStore injects the [kwh.SourceStore] used by all inbound
// webhook receivers to look up HMAC secrets by source ID. A nil or typed-nil
// value is silently ignored; the final nil check happens during phase5 when
// any cell has declared a webhook receiver.
//
// Typical usage: pass a pre-populated [kwh.NewSourceRegistry] that has been
// seeded with [kwh.Source] values for each expected sender.
func WithWebhookSourceStore(store kwh.SourceStore) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(store) {
			return
		}
		b.webhookSourceStore = store
	}
}

// WithWebhookClaimer injects the [idempotency.Claimer] used by all inbound
// webhook receivers to deduplicate deliveries. A nil or typed-nil value is
// silently ignored; the final nil check happens during phase5 when any cell
// has declared a webhook receiver.
//
// For tests, [idempotency.NewInMemClaimer] with the bootstrap clock is
// sufficient. Production deployments should use a distributed claimer backed
// by Redis or PostgreSQL.
func WithWebhookClaimer(claimer idempotency.Claimer) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(claimer) {
			return
		}
		b.webhookClaimer = claimer
	}
}
