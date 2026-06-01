package webhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// defaultDeliveryTimeout bounds a single outbound delivery attempt. 30s sits at
// the upper end of the standard-webhooks recommendation (15–30s) and matches
// Convoy's default; Svix uses 15s. The generous default gives downstream
// receivers room to process synchronously; tighten via WithDeliveryTimeout.
const defaultDeliveryTimeout = 30 * time.Second

// dispatchBodyDrainLimit bounds how much of a response body is drained to allow
// HTTP keep-alive connection reuse. The body is otherwise irrelevant — only the
// status code drives the disposition.
const dispatchBodyDrainLimit = 4 << 10 // 4 KiB

// Dispatcher delivers an outbound webhook for each consumed outbox [outbox.Entry]:
// it selects the target URL, vets it against the SSRF policy, HMAC-signs the
// payload, POSTs it, and maps the outcome to an [outbox.HandleResult]. It
// implements [outbox.EntryHandler] via [Dispatcher.Handle].
//
// Construct via [NewDispatcher]; the zero value is invalid. All HTTP egress
// flows through an *http.Client built from the injected [SafePolicy] — there is
// no client-injection option, so SSRF protection cannot be bypassed
// (WEBHOOK-SSRF-GUARD-01 downstream; see tools/archtest/webhook_ssrf_guard_test.go).
type Dispatcher struct {
	clk      clock.Clock
	signer   Signer
	policy   *SafePolicy
	selector WebhookDispatchSelector
	client   *http.Client
	timeout  time.Duration
}

// DispatcherOption customizes a Dispatcher at construction time.
type DispatcherOption func(*Dispatcher)

// WithDeliveryTimeout overrides the per-attempt delivery timeout
// (default defaultDeliveryTimeout). A non-positive value is ignored.
func WithDeliveryTimeout(d time.Duration) DispatcherOption {
	return func(dp *Dispatcher) {
		if d > 0 {
			dp.timeout = d
		}
	}
}

// NewDispatcher constructs a Dispatcher. clk is the mandatory positional clock
// (CLOCK-POSITIONAL-INJECTION-01). signer, policy, and selector are mandatory;
// a nil argument returns an [errcode.ErrWebhookConfigInvalid] error. The
// outbound *http.Client is always built internally from policy — its
// DialContext (resolve→vet→dial) and DenyRedirect (3xx deny-all) are wired so
// every request is SSRF-vetted.
func NewDispatcher(
	clk clock.Clock,
	signer Signer,
	policy *SafePolicy,
	selector WebhookDispatchSelector,
	opts ...DispatcherOption,
) (*Dispatcher, error) {
	clock.MustHaveClock(clk, "webhook.NewDispatcher")
	if validation.IsNilInterface(signer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatcher: signer must not be nil")
	}
	if policy == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatcher: SSRF policy must not be nil")
	}
	if selector == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook dispatcher: target selector must not be nil")
	}
	d := &Dispatcher{
		clk:      clk,
		signer:   signer,
		policy:   policy,
		selector: selector,
		timeout:  defaultDeliveryTimeout,
	}
	for _, o := range opts {
		o(d)
	}
	// Build the client AFTER options so WithDeliveryTimeout is honored. The
	// transport's DialContext and the CheckRedirect both come from the SSRF
	// policy; there is intentionally no way to inject a foreign client.
	d.client = &http.Client{
		Timeout:       d.timeout,
		Transport:     &http.Transport{DialContext: policy.DialContext},
		CheckRedirect: policy.DenyRedirect,
	}
	return d, nil
}

// Handle implements [outbox.EntryHandler]: it delivers one outbound webhook and
// maps the outcome to a disposition via [Classify].
//
// Consumer: cg-webhook-dispatch (per-cell consumer group)
// Idempotency: outbox lease_id CAS (producer side); handler is stateless
// Disposition: Ack on 2xx / Requeue on 5xx·408·429·transport / Reject on other 4xx·3xx·SSRF
// DLX: broker-native via DispositionReject -> Nack(requeue=false)
//
//	2xx                              → Ack
//	5xx / 408 / 429 / timeout / conn → Requeue (transient)
//	other 4xx / 3xx / SSRF-blocked   → Reject  (permanent → DLX)
func (d *Dispatcher) Handle(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	req, fail := d.prepare(ctx, entry)
	if req == nil {
		return fail
	}
	deliveryID := entry.ID()
	resp, err := d.client.Do(req)
	if err != nil {
		reason := transportReason(err)
		result := mapDisposition(Classify(0, err), reason)
		logDelivery(ctx, deliveryID, result.Disposition, reason)
		return result
	}
	defer drainAndClose(resp)
	reason := statusReason(resp.StatusCode)
	result := mapDisposition(Classify(resp.StatusCode, nil), reason)
	logDelivery(ctx, deliveryID, result.Disposition, reason)
	return result
}

// logDelivery emits a structured slog record for non-Ack delivery outcomes.
// delivery_id is the outbox entry ID used as the per-delivery idempotency key.
// Payload, Source, and Signer are never logged to avoid secret leakage.
func logDelivery(ctx context.Context, deliveryID string, disp outbox.Disposition, err error) {
	switch disp {
	case outbox.DispositionReject:
		slog.ErrorContext(ctx, "webhook dispatcher: permanent delivery failure",
			slog.String("delivery_id", deliveryID),
			slog.Any("error", err))
	case outbox.DispositionRequeue:
		slog.WarnContext(ctx, "webhook dispatcher: transient delivery failure",
			slog.String("delivery_id", deliveryID),
			slog.Any("error", err))
	}
}

// prepare runs the per-entry preflight (select target → vet URL → derive
// delivery id → sign → build request). On success it returns the request and a
// zero HandleResult; on failure it returns a nil request and the Reject result
// to return. Splitting this out keeps Handle's cognitive complexity low.
func (d *Dispatcher) prepare(ctx context.Context, entry outbox.Entry) (*http.Request, outbox.HandleResult) {
	payload := entry.Payload()

	target, err := d.selector(ctx, payload)
	if err != nil {
		return nil, outbox.Reject(errcode.Wrap(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
			"webhook dispatcher: target selector failed", err))
	}
	if err := d.policy.ValidateTargetURL(target); err != nil {
		return nil, outbox.Reject(err) // already ErrWebhookSSRFBlocked
	}

	deliveryID, err := NewDeliveryID(entry.ID())
	if err != nil {
		return nil, outbox.Reject(errcode.Wrap(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
			"webhook dispatcher: outbox entry id is not a valid delivery id", err))
	}
	headers, err := d.signer.Sign(payload, d.clk.Now(), deliveryID)
	if err != nil {
		return nil, outbox.Reject(errcode.Wrap(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
			"webhook dispatcher: signing failed", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, outbox.Reject(errcode.Wrap(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
			"webhook dispatcher: build request failed", err))
	}
	req.Header.Set("Content-Type", "application/json")
	headers.Apply(req.Header) // SOLE sanctioned signature-header writer.
	return req, outbox.HandleResult{}
}

// mapDisposition converts a [Classify] disposition to an [outbox.HandleResult].
// reason is the diagnostic error for the non-Ack branches; it is ignored for
// Ack.
func mapDisposition(disp outbox.Disposition, reason error) outbox.HandleResult {
	switch disp {
	case outbox.DispositionAck:
		return outbox.Ack()
	case outbox.DispositionReject:
		return outbox.Reject(reason)
	default:
		// DispositionRequeue + any unknown future disposition -> Requeue (fail-closed; never silently Ack)
		return outbox.Requeue(reason)
	}
}

// transportReason builds the diagnostic error for a failed client.Do. An SSRF
// rejection (already an *errcode.Error tagged ErrWebhookSSRFBlocked) is passed
// through; any other transport fault becomes a transient delivery error.
func transportReason(err error) error {
	var ee *errcode.Error
	if errors.As(err, &ee) && ee.Code == errcode.ErrWebhookSSRFBlocked {
		return ee
	}
	return errcode.Wrap(errcode.KindUnavailable, errcode.ErrWebhookDeliveryFailed,
		"webhook dispatcher: delivery transport error", err)
}

// statusReason builds the diagnostic error for a non-2xx response. The status
// code is server-side only (Internal) — it is not wire-relevant for a dispatch
// consumer. Transient vs permanent mirrors Classify.
func statusReason(status int) error {
	transient := status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests || status >= 500
	if transient {
		return errcode.New(errcode.KindUnavailable, errcode.ErrWebhookDeliveryFailed,
			"webhook dispatcher: transient non-success delivery response",
			errcode.WithInternal(errcode.InternalAttr("status_code", status)))
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
		"webhook dispatcher: permanent non-success delivery response",
		errcode.WithInternal(errcode.InternalAttr("status_code", status)))
}

// drainAndClose drains a bounded prefix of the response body (for keep-alive
// reuse) and closes it.
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, dispatchBodyDrainLimit))
	_ = resp.Body.Close()
}

// Compile-time assertion: Dispatcher.Handle satisfies outbox.EntryHandler.
var _ outbox.EntryHandler = (*Dispatcher)(nil).Handle
