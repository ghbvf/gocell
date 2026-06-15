// Package dispatch wires outbound-webhook dispatchers into the event router.
//
// An outbound webhook is modeled as an event subscription: the producing cell
// emits a domain event to the broker on a topic equal to the webhook-dispatch
// contract ID, and this package registers a [kwh.Dispatcher] as that
// subscription's handler. Each consumed [outbox.Entry] is signed (HMAC-SHA256,
// vendor-neutral webhook-* headers) and POSTed to the selector-resolved target
// through an SSRF-guarded *http.Client. Delivery outcomes map to outbox
// dispositions via [kwh.Classify] (standard-webhooks aligned: 2xx Ack / every
// non-2xx + transient transport Requeue / SSRF-blocked Reject).
//
// This mirrors the inbound receiver pattern (runtime/webhook/registry.go
// BuildRouteGroups) but the dispatch side consumes from the broker rather than
// mounting HTTP routes, so it drains through the event router (phase6) rather
// than the HTTP route-group drain (phase5).
//
// # Troubleshooting
//
// "webhook dispatchers declared but no source store configured":
// Bootstrap failed because at least one cell registered a webhook-dispatch
// contract but WithWebhookSourceStore was not passed to bootstrap. Add the
// option and provide a populated [kwh.SourceStore].
//
// "webhook dispatch: signing source not registered" (sourceId in error details):
// BuildConsumers found no entry in the SourceStore for the SourceID declared in
// DispatchSpec. The missing sourceId is reported in the error details. Register
// the source secret in the store before starting.
//
// "no cell.go struct field for subscribing slice" from gocell generate cell:
// The cell struct lacks a field whose pointer type package name matches the
// webhook-dispatch slice ID. Add *<sliceID>.Consumer (or equivalent) to the
// cell struct and re-run codegen.
//
// "ambiguous field for slice <sliceID>" from gocell generate cell:
// Multiple cell struct fields match the slice ID. Add field: <fieldName> to
// the webhook-dispatch contractUsage in slice.yaml to disambiguate.
package dispatch

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	kwh "github.com/ghbvf/gocell/framework/kernel/webhook"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/internal/contractbuild"
)

// Consumer is a built outbound-webhook dispatcher paired with the subscription
// identity needed to register it on the event router via AddContractHandler.
type Consumer struct {
	// Spec is the synthesized event-kind ContractSpec the router subscribes on.
	// Topic == the webhook-dispatch contract ID: the producing cell emits to
	// that topic, this consumer signs and POSTs each delivered entry.
	Spec contractspec.ContractSpec
	// Handler is the dispatcher's Handle method (an outbox.EntryHandler).
	Handler outbox.EntryHandler
	// ConsumerGroup and CellID are both the owning cell ID: replicas of the
	// same cell compete for delivery (one POST per entry), and the cell is the
	// observability owner.
	ConsumerGroup string
	CellID        string
	// BrokerDelaySchedule is the Svix per-attempt retry timeline
	// (DefaultSvixSchedule().Delays()) the bootstrap drain copies onto the
	// outbox.Subscription so the broker honors per-attempt delays (#1458).
	BrokerDelaySchedule []time.Duration
}

// Validate checks that all required Consumer fields are populated. buildConsumer
// calls this as a belt-and-suspenders guard before returning, consistent with
// ReceiverSpec.Validate and DispatchSpec.Validate.
func (c Consumer) Validate() error {
	if c.Spec.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch consumer: Spec.ID must not be empty")
	}
	if c.Handler == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch consumer: Handler must not be nil")
	}
	if c.ConsumerGroup == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch consumer: ConsumerGroup must not be empty")
	}
	if c.CellID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch consumer: CellID must not be empty")
	}
	return nil
}

// BuildConsumers turns the accumulated [cell.WebhookDispatchRequest] values
// (from RegistrySnapshot.WebhookDispatchers) into [Consumer] values ready for
// the bootstrap phase6 drain to register on the event router.
//
// clk is the mandatory positional clock (CLOCK-POSITIONAL-INJECTION-01). store
// resolves each request's signing secret by SourceID; policy is the shared
// SSRF guard wired into every dispatcher's *http.Client. Both must be non-nil.
// Construction is eager so a missing source or bad config fails at startup
// rather than at first delivery.
//
// cbSettings controls the per-endpoint circuit-breaker thresholds for every
// dispatcher built by this call. The zero value is invalid; pass
// [kwh.DefaultCircuitBreakerSettings] when no override is configured.
//
// DLX: the composition root must configure the broker subscriber's DLX exchange
// for dispatch subscription topics; permanently-failed deliveries (Reject) are
// Nack(requeue=false)→DLX and are otherwise silently discarded.
// rec is the optional dispatch-side metrics recorder (the zero value disables
// recording); the bootstrap auto-wire passes the registered collector when a
// metrics provider is configured.
func BuildConsumers(
	clk clock.Clock,
	reqs []cell.WebhookDispatchRequest,
	store kwh.SourceStore,
	policy *kwh.SafePolicy,
	rec kwh.Metrics,
	cbSettings kwh.CircuitBreakerSettings,
) ([]Consumer, error) {
	clock.MustHaveClock(clk, "webhook/dispatch.BuildConsumers")
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch: source store must not be nil")
	}
	if policy == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch: SSRF policy must not be nil")
	}
	// Validate settings here so a bad value from the caller fails eagerly at
	// BuildConsumers time (startup) rather than per-dispatcher construction.
	// cbSettings.Validate() already returns a well-formed errcode (KindInvalid +
	// ErrWebhookConfigInvalid + typed WithDetails), so propagate it directly
	// rather than re-minting a new errcode with a dynamic message string.
	if err := cbSettings.Validate(); err != nil {
		return nil, err
	}
	out := make([]Consumer, 0, len(reqs))
	for _, req := range reqs {
		c, err := buildConsumer(clk, req, store, policy, rec, cbSettings)
		if err != nil {
			return nil, fmt.Errorf("webhook dispatch: contract %q: %w", req.Spec.ContractID, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// buildConsumer constructs a single Consumer: resolve source → HMAC signer →
// SSRF-guarded Dispatcher → synthesized event-kind subscription spec.
//
// Producer contract: Topic == spec.ContractID — the producing cell MUST emit
// outbox entries on a broker topic whose value equals the webhook-dispatch
// contract ID (same convention as event subscriptions).
func buildConsumer(
	clk clock.Clock,
	req cell.WebhookDispatchRequest,
	store kwh.SourceStore,
	policy *kwh.SafePolicy,
	rec kwh.Metrics,
	cbSettings kwh.CircuitBreakerSettings,
) (Consumer, error) {
	spec := req.Spec
	if err := spec.Validate(); err != nil {
		return Consumer{}, err
	}
	if req.Selector == nil {
		return Consumer{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch: target selector must not be nil")
	}
	sourceID, err := kwh.NewSourceID(spec.SourceID)
	if err != nil {
		return Consumer{}, err
	}
	src, ok := store.Lookup(sourceID)
	if !ok {
		return Consumer{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatch: signing source not registered",
			errcode.WithDetails(errcode.PublicString("sourceId", spec.SourceID)),
			errcode.WithInternal(errcode.InternalAttr("source_id", spec.SourceID)))
	}
	signer, err := kwh.NewHMACSigner(src)
	if err != nil {
		return Consumer{}, err
	}
	dispatcher, err := kwh.NewDispatcher(clk, signer, policy, req.Selector,
		kwh.WithMetrics(rec, spec.SourceID),
		kwh.WithCircuitBreakerSettings(cbSettings))
	if err != nil {
		return Consumer{}, err
	}
	cs, err := contractbuild.NewWebhookDispatch(spec)
	if err != nil {
		return Consumer{}, err
	}
	c := Consumer{
		Spec:                cs,
		Handler:             dispatcher.Handle,
		ConsumerGroup:       spec.CellID,
		CellID:              spec.CellID,
		BrokerDelaySchedule: kwh.DefaultSvixSchedule().Delays(),
	}
	if err := c.Validate(); err != nil {
		return Consumer{}, err
	}
	return c, nil
}
