package bootstrap

// options_webhook.go — With* option functions for webhook wiring.
//
// Covers: WithWebhookSourceStore (inbound receivers phase5 AND outbound
// dispatchers phase6), WithWebhookClaimer (inbound receiver), WithWebhookSSRFPolicy
// (outbound dispatcher).
//
// WithWebhookSourceStore and WithWebhookClaimer are "cumulative builder" options
// (see runtime-api.md §Option 范式分层): nil inputs are silently ignored; the
// final nil check happens at the consuming phase.
//
// Design note — fail-fast timing: the missing-dependency error is deliberately
// raised at the consuming phase (phase5 when inbound receivers are drained;
// phase6 when outbound dispatchers are built) rather than at option-apply time.
// A deployment with zero webhook receivers AND zero dispatchers is a valid
// configuration that needs neither a SourceStore nor a Claimer, so requiring
// them unconditionally at option time would reject correct setups. The
// dependency only becomes mandatory once a cell has actually declared a receiver
// (phase5) or a dispatcher (phase6) — exactly when that phase can see both the
// snapshot and the wired options. This matches the builder-option convention
// used elsewhere in bootstrap (final validation at the consuming phase, not at
// the setter).

import (
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/framework/kernel/webhook"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// WithWebhookSourceStore injects the [kwh.SourceStore] used to look up HMAC
// secrets by source ID. It serves BOTH directions: inbound webhook receivers
// (phase5) verify incoming signatures against it, and outbound webhook
// dispatchers (phase6) resolve each dispatcher's signing secret from it
// (inbound: BuildRouteGroups → SourceStore.Lookup; outbound: BuildConsumers →
// SourceStore.Lookup). A nil or typed-nil value is silently
// ignored; the final nil check happens at the consuming phase — phase5 if any
// cell declared a receiver, phase6 if any cell declared a dispatcher.
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

// WithWebhookSSRFPolicy injects the [kwh.SafePolicy] wired into every outbound
// webhook dispatcher's *http.Client. A nil value is silently ignored; when any
// cell registers a webhook dispatcher and no policy was set, phase6 builds the
// default production policy [kwh.NewSafePolicy] (block all private/reserved
// ranges, no loopback). Override only for dev / CI — e.g.
// kwh.NewSafePolicy(kwh.WithAllowLoopback()) to deliver to a local test server.
//
// Cumulative builder option (see runtime-api.md §Option 范式分层): the final
// decision happens at phase6 drain, not at option-apply time, because a
// deployment with zero dispatchers needs no policy.
func WithWebhookSSRFPolicy(policy *kwh.SafePolicy) Option {
	return func(b *Bootstrap) {
		if policy == nil {
			return
		}
		b.webhookSSRFPolicy = policy
	}
}

// WithWebhookCircuitBreaker overrides the per-endpoint circuit-breaker
// thresholds applied to every outbound webhook dispatcher in this deployment.
// All three fields (TripThreshold, OpenTimeout, HalfOpenProbes) must be
// positive; a zero or negative value is rejected immediately at option-apply
// time (fail-fast — per runtime-api.md §Option 范式: "强依赖 option 必须
// fail-fast，不静默 noop"). Omitting this option uses the defaults that match
// the original hardcoded values (TripThreshold=5, OpenTimeout=60s,
// HalfOpenProbes=1).
//
// There is intentionally no Enabled/Disabled variant: disabling the circuit
// breaker is not a supported configuration (no-disable invariant).
//
// Bootstrap panics at option-apply time on invalid settings so misconfiguration
// is visible on startup rather than silently degrading to defaults or first
// delivery. The panic uses [panicregister.Approved] as required by the
// PANIC-REGISTERED-01 governance rule.
func WithWebhookCircuitBreaker(settings kwh.CircuitBreakerSettings) Option {
	return func(b *Bootstrap) {
		if err := settings.Validate(); err != nil {
			// Fail-fast at option-apply time: invalid CB settings are a
			// programmer error (B-class assertion), not a runtime degradation
			// path. Panics through the panic-taxonomy funnel so PANIC-REGISTERED-01
			// archtest does not flag this as an unapproved panic site.
			panic(panicregister.Approved("bootstrap-webhook-cb-invalid-settings",
				errcode.Assertion("bootstrap: WithWebhookCircuitBreaker: %s", err.Error())))
		}
		b.webhookCBSettings = settings
		b.webhookCBSettingsSet = true
	}
}
