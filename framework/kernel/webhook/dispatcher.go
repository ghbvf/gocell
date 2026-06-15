package webhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
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
	// circuit is the per-endpoint circuit breaker, always enabled (no production
	// opt-out per the resilience posture): once a target's breaker trips it is
	// fast-failed (Requeue) instead of POSTed. See circuit.go.
	circuit *circuitGate
	// recorder + source are the optional observability dependency (PR-6). The
	// zero Metrics value disables every record (no-op); source is the {source}
	// label, set together via WithMetrics. Each Dispatcher is bound to one source
	// (buildConsumer constructs one per webhook-dispatch contract).
	recorder Metrics
	source   string
	// cbSettings holds the circuit-breaker thresholds supplied via
	// WithCircuitBreakerSettings. cbSettingsSet distinguishes "option provided
	// with specific values" from "option not provided → use defaults".
	cbSettings    CircuitBreakerSettings
	cbSettingsSet bool
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

// WithMetrics injects the optional delivery observability recorder and the
// {source} label this dispatcher records under. Omitting it leaves the zero
// Metrics value, which records as a no-op (the disabled / NopProvider case).
func WithMetrics(rec Metrics, source string) DispatcherOption {
	return func(dp *Dispatcher) {
		dp.recorder = rec
		dp.source = source
	}
}

// WithCircuitBreakerSettings overrides the per-endpoint circuit-breaker
// thresholds for this dispatcher. All three fields (TripThreshold, OpenTimeout,
// HalfOpenProbes) must be positive; a zero or negative value causes
// [NewDispatcher] to return an error (fail-fast — no silent noop, per
// runtime-api.md §Option 范式). Omitting this option uses the defaults that
// match the original hardcoded values (TripThreshold=5, OpenTimeout=60s,
// HalfOpenProbes=1).
//
// There is intentionally no Enabled/Disabled field: disabling the circuit
// breaker is not a supported configuration (no-disable invariant).
func WithCircuitBreakerSettings(s CircuitBreakerSettings) DispatcherOption {
	return func(dp *Dispatcher) {
		dp.cbSettings = s
		dp.cbSettingsSet = true
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
	// Resolve circuit-breaker settings AFTER options: if WithCircuitBreakerSettings
	// was provided, validate and use it; otherwise fall back to defaults.
	cbSettings := DefaultCircuitBreakerSettings()
	if d.cbSettingsSet {
		if err := d.cbSettings.Validate(); err != nil {
			return nil, err
		}
		cbSettings = d.cbSettings
	}
	d.circuit = newCircuitGate(clk, cbSettings)
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
// Disposition: Ack on 2xx / Requeue on every non-2xx + transient transport / Reject on SSRF-blocked + permanent prepare failure
// DLX: broker-native via DispositionReject -> Nack(requeue=false)
//
//	endpoint circuit open                   → Requeue (fast-fail, no HTTP attempt)
//	2xx                                    → Ack
//	non-2xx (3xx/4xx/5xx) / timeout / conn  → Requeue (transient; standard-webhooks aligned)
//	SSRF-blocked transport / bad config     → Reject  (permanent → DLX)
func (d *Dispatcher) Handle(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	req, fail := d.prepare(ctx, entry)
	if req == nil {
		// Prepare failed before any HTTP attempt (SSRF pre-flight reject /
		// permanent or transient prepare failure): record the outcome but no
		// duration sample (no delivery was attempted).
		d.recorder.recordDelivery(ctx, d.source, dispositionResult(fail.Disposition))
		return fail
	}
	// Gate the HTTP attempt on the per-(tenant, endpoint) circuit breaker. An
	// open circuit fast-fails without a POST: Requeue (not Reject) so the outbox
	// redelivers per the Svix schedule once the breaker may probe again —
	// composing with, not replacing, the terminal MaxRetries→DLX path. The key
	// includes the entry's TenantID so a shared target URL cannot let one
	// tenant's failures fast-fail another tenant's deliveries (cross-tenant
	// isolation; empty tenant = tenantless system delivery → _notenant sentinel).
	endpoint := req.URL.String()
	key := newCircuitEndpointKey(entry.Principal().TenantID.String(), endpoint)
	allow, done := d.circuit.Allow(key)
	if !allow {
		d.recorder.recordDelivery(ctx, d.source, deliveryCircuitOpen)
		return outbox.Requeue(errcode.New(errcode.KindUnavailable, errcode.ErrCircuitOpen,
			"webhook dispatcher: endpoint circuit open"))
	}
	deliveryID := entry.ID()
	start := d.clk.Now()
	resp, err := d.client.Do(req)
	d.recorder.observeDeliveryDuration(ctx, d.source, d.clk.Now().Sub(start).Seconds())
	if err != nil {
		disp := Classify(0, err)
		reason := transportReason(err)
		done(circuitProbeOutcome(0, err))
		d.recorder.recordDelivery(ctx, d.source, dispositionResult(disp))
		logDelivery(ctx, deliveryID, disp, reason)
		return mapDisposition(disp, reason)
	}
	defer func() { _ = resp.Body.Close() }()
	summary := drainBodySummary(resp.Body)
	disp := Classify(resp.StatusCode, nil)
	done(circuitProbeOutcome(resp.StatusCode, nil))
	d.recorder.recordDelivery(ctx, d.source, statusResult(resp.StatusCode))
	if disp == outbox.DispositionAck {
		return outbox.Ack()
	}
	reason := statusReason(resp.StatusCode, summary)
	logDelivery(ctx, deliveryID, disp, reason)
	return mapDisposition(disp, reason)
}

// dispositionResult maps a non-HTTP-response outcome (a prepare failure or a
// transport fault) to the metric result label. A Reject disposition is the SSRF
// pre-flight block / permanent prepare failure (deliveryBlocked); any other
// disposition is a transient failure with no HTTP response (deliveryTransportError).
func dispositionResult(disp outbox.Disposition) webhookDeliveryResult {
	if disp == outbox.DispositionReject {
		return deliveryBlocked
	}
	return deliveryTransportError
}

// statusResult maps a received HTTP status code to the metric result label. 2xx
// is success; 5xx is a downstream-transient server_error; every other response
// (4xx, and the rare 1xx/3xx that reaches here) is a client_error. 3xx is
// normally intercepted by the SSRF redirect-deny and surfaces as a transport
// fault instead, so it seldom lands in this branch.
func statusResult(code int) webhookDeliveryResult {
	switch {
	case code >= 200 && code < 300:
		return deliverySuccess
	case code >= 500:
		return deliveryServerError
	default:
		return deliveryClientError
	}
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
		return nil, selectorReason(err)
	}
	if err := d.policy.ValidateTargetURL(target); err != nil {
		return nil, outbox.Reject(err) // already ErrWebhookSSRFBlocked
	}

	deliveryID, err := NewDeliveryID(entry.ID())
	if err != nil {
		return nil, outbox.Reject(errcode.Wrap(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
			"webhook dispatcher: outbox entry id is not a valid delivery id", err))
	}
	signed, err := d.signer.Sign(payload, d.clk.Now(), deliveryID)
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
	signed.Apply(req.Header) // SOLE sanctioned signature-header writer.
	return req, outbox.Ack()
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

// selectorReason maps a target-selector error to a disposition result. A
// selector that signals ErrWebhookPermanentFailure (e.g. no subscription is
// configured for the event) is permanent → Reject; any other error (a store /
// config lookup hiccup) is transient → Requeue, so an infrastructure blip does
// not dead-letter the delivery. Mirrors the SSRF-tag inspection in
// transportReason: the disposition is driven by the error's errcode, not by a
// blanket assumption that all selector failures are permanent.
func selectorReason(err error) outbox.HandleResult {
	var ee *errcode.Error
	if errors.As(err, &ee) && ee.Code == errcode.ErrWebhookPermanentFailure {
		return outbox.Reject(ee)
	}
	return outbox.Requeue(errcode.Wrap(errcode.KindUnavailable, errcode.ErrWebhookDeliveryFailed,
		"webhook dispatcher: target selector failed", err))
}

// statusReason builds the transient diagnostic error for a non-2xx response. Per
// Classify (standard-webhooks aligned) every non-2xx status is a transient
// delivery failure, so there is a single outcome here. The status code and the
// bounded response-body summary are server-side only (Internal) — never
// wire-relevant for a dispatch consumer — and are sink-redacted in slog; they
// aid debugging which receiver rejected and why.
func statusReason(status int, bodySummary string) error {
	return errcode.New(errcode.KindUnavailable, errcode.ErrWebhookDeliveryFailed,
		"webhook dispatcher: non-success delivery response",
		errcode.WithInternal(
			errcode.InternalAttr("status_code", status),
			errcode.InternalAttr("response_body", bodySummary)))
}

// drainBodySummary reads a bounded prefix of the response body
// (≤ dispatchBodyDrainLimit) so the connection can be reused for keep-alive and
// returns it as a diagnostic summary. The summary is attached to the non-2xx
// Internal diagnostic (server-side slog only, sink-redacted, never on the wire).
func drainBodySummary(body io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(body, dispatchBodyDrainLimit))
	return string(b)
}

// Compile-time assertion: Dispatcher.Handle satisfies outbox.EntryHandler.
var _ outbox.EntryHandler = (*Dispatcher)(nil).Handle
