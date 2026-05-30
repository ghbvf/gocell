package bootstrap

// options_webhook.go — With* option functions for inbound-webhook receiver wiring.
//
// Covers: WithWebhookSourceStore, WithWebhookClaimer.
//
// Both are "cumulative builder" options (see runtime-api.md §Option 范式分层):
// nil inputs are silently ignored; the final nil check happens inside
// phase5DrainWebhookReceivers when a cell has registered receivers.
//
// Design note — fail-fast timing: the missing-dependency error is deliberately
// raised at phase5 (when receivers are drained) rather than at option-apply
// time. A deployment with zero webhook receivers is a valid configuration that
// needs neither a SourceStore nor a Claimer, so requiring them unconditionally
// at option time would reject correct setups. The dependency only becomes
// mandatory once a cell has actually declared a receiver — which is exactly when
// phase5 can see both the snapshot and the wired options. This matches the
// builder-option convention used elsewhere in bootstrap (final validation at the
// consuming phase, not at the setter).

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
