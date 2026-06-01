// Package dispatch wires outbound-webhook dispatchers into the event router.
//
// An outbound webhook is modeled as an event subscription: the producing cell
// emits a domain event to the broker on a topic equal to the webhook-dispatch
// contract ID, and this package registers a [kwh.Dispatcher] as that
// subscription's handler. Each consumed [outbox.Entry] is signed (HMAC-SHA256,
// vendor-neutral webhook-* headers) and POSTed to the selector-resolved target
// through an SSRF-guarded *http.Client. Delivery outcomes map to outbox
// dispositions via [kwh.Classify] (2xx Ack / 5xx·408·429·timeout Requeue /
// other 4xx·3xx·SSRF Reject).
//
// This mirrors the inbound receiver pattern (runtime/webhook/registry.go
// BuildRouteGroups) but the dispatch side consumes from the broker rather than
// mounting HTTP routes, so it drains through the event router (phase6) rather
// than the HTTP route-group drain (phase5).
package dispatch

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// dispatchTransport is the broker transport for webhook-dispatch subscriptions.
// The dispatcher consumes outbound-intent entries from the outbox broker, same
// as any event subscription.
const dispatchTransport = "amqp"

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
func BuildConsumers(
	clk clock.Clock,
	reqs []cell.WebhookDispatchRequest,
	store kwh.SourceStore,
	policy *kwh.SafePolicy,
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
	out := make([]Consumer, 0, len(reqs))
	for _, req := range reqs {
		c, err := buildConsumer(clk, req, store, policy)
		if err != nil {
			return nil, fmt.Errorf("webhook dispatch: contract %q: %w", req.Spec.ContractID, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// buildConsumer constructs a single Consumer: resolve source → HMAC signer →
// SSRF-guarded Dispatcher → synthesized event-kind subscription spec.
func buildConsumer(
	clk clock.Clock,
	req cell.WebhookDispatchRequest,
	store kwh.SourceStore,
	policy *kwh.SafePolicy,
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
			errcode.WithInternal(errcode.InternalAttr("source_id", spec.SourceID)))
	}
	signer, err := kwh.NewHMACSigner(src)
	if err != nil {
		return Consumer{}, err
	}
	dispatcher, err := kwh.NewDispatcher(clk, signer, policy, req.Selector)
	if err != nil {
		return Consumer{}, err
	}
	return Consumer{
		Spec: contractspec.ContractSpec{
			ID:        spec.ContractID,
			Kind:      cellvocab.ContractEvent,
			Transport: dispatchTransport,
			Topic:     spec.ContractID,
		},
		Handler:       dispatcher.Handle,
		ConsumerGroup: spec.CellID,
		CellID:        spec.CellID,
	}, nil
}
