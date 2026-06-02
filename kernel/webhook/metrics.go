package webhook

import (
	"context"
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// Webhook metric family names. Single source so the Dispatcher (dispatch side),
// the runtime Receiver (receive side), RegisterMetrics, and any dashboard/alert
// reference the same identifiers.
//
// Naming aligns with the OTel webhook-delivery convention
// (webhook.delivery.*) and Convoy's convoy_event_delivery_* family; see the
// PR-6 open-source benchmark.
const (
	metricWebhookDeliveriesTotal   = "webhook_deliveries_total"
	metricWebhookDeliveryDuration  = "webhook_delivery_duration_seconds"
	metricWebhookSignatureFailures = "webhook_signature_failures_total"
	metricWebhookIdempotencyHits   = "webhook_idempotency_hits_total"
)

// Webhook metric label names.
const (
	labelResult = "result"
	labelSource = "source"
	labelReason = "reason"
)

// webhookDeliveryResult is the sealed (package-private) string type for the
// webhook_deliveries_total{result=...} label. recordDelivery deals only in
// webhookDeliveryResult, so the value set is enumerable by TYPE (not by name
// prefix). The remaining hole — an untyped string literal is assignable to
// webhookDeliveryResult — is closed downstream by the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 callsite guard, which bans any
// recordDelivery argument that is not one of the declared delivery* consts.
//
// The taxonomy is status-aware rather than a 3-way disposition collapse: the
// kernel Classify maps every non-2xx + transient transport to Requeue, but a
// metric that folds 4xx and 5xx into one bucket loses SLO signal (an endpoint
// misconfiguration flood vs. a downstream outage). Aligned to Alertmanager
// notify (clientError/serverError split), Kubernetes admission webhook
// (error_type), and Convoy (Discarded). The label therefore carries finer
// information than the outbox Disposition — the same shape as Convoy's
// status+http_status_code and K8s' code+rejected dimensions.
type webhookDeliveryResult string

const (
	// deliverySuccess: a 2xx response (Disposition=Ack).
	deliverySuccess webhookDeliveryResult = "success"
	// deliveryClientError: a 4xx (or other non-2xx HTTP) response — endpoint
	// config/auth issue (Disposition=Requeue, standard-webhooks aligned).
	deliveryClientError webhookDeliveryResult = "client_error"
	// deliveryServerError: a 5xx response — downstream transient
	// (Disposition=Requeue).
	deliveryServerError webhookDeliveryResult = "server_error"
	// deliveryTransportError: connection failure / timeout with no HTTP
	// response received (Disposition=Requeue).
	deliveryTransportError webhookDeliveryResult = "transport_error"
	// deliveryBlocked: SSRF pre-flight reject or a permanent prepare failure
	// (Disposition=Reject → DLX).
	deliveryBlocked webhookDeliveryResult = "blocked"
)

// SignatureFailureReason is the exported sealed string type for the
// webhook_signature_failures_total{reason=...} label. It is exported because
// the receive-side classification happens in runtime/webhook (the Receiver's
// verify step), which returns a typed reason that flows into
// [Metrics.RecordSignatureFailure]. The value set is enumerable by TYPE; the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 archtest freezes it.
//
// The wire 401 is deliberately uniform for ReasonUnknownSource and
// ReasonBadSignature (anti-enumeration oracle, see the Receiver verify godoc),
// but the server-side metric distinguishes them — the metric is not a wire
// oracle, and missing/invalid/unknown/bad/expired enable distinct alerts (the
// Kubernetes admission error_type / Vault audit reason precedent).
type SignatureFailureReason string

const (
	// ReasonMissingHeader: one or more required signature headers were absent.
	ReasonMissingHeader SignatureFailureReason = "missing_header"
	// ReasonInvalidHeader: a header was present but unparseable (bad delivery
	// id, non-integer timestamp).
	ReasonInvalidHeader SignatureFailureReason = "invalid_header"
	// ReasonUnknownSource: the configured source id was not registered in the
	// source store.
	ReasonUnknownSource SignatureFailureReason = "unknown_source"
	// ReasonBadSignature: the HMAC did not match for any candidate secret.
	ReasonBadSignature SignatureFailureReason = "bad_signature"
	// ReasonTimestampExpired: the timestamp skew exceeded the tolerance window.
	ReasonTimestampExpired SignatureFailureReason = "timestamp_expired"
)

// webhookDeliveryDurationBuckets are explicit upper bounds (seconds) for
// webhook_delivery_duration_seconds. Outbound webhook delivery spans fast
// successes (50–500ms) to slow responses near the delivery timeout (default
// 30s, the standard-webhooks 15–30s recommendation). The bucket set therefore
// extends to 30s, mirroring the Kubernetes admission-webhook histogram (which
// adds 10/25s for webhook timeouts) rather than the kernel reconcile buckets
// (sub-ms→minute internal work). Supplied explicitly because the metric leaves
// kernel (HistogramOpts godoc: callers should not rely on adapter defaults).
var webhookDeliveryDurationBuckets = []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30}

// Metrics holds the optional pre-bound webhook instruments. A nil field
// disables that instrument (every record method nil-checks before recording),
// so a zero Metrics value — the disabled / NopProvider case — is a safe no-op.
// Build via RegisterMetrics at the composition root (bootstrap auto-wire) and
// inject into both the Dispatcher (dispatch side) and the runtime Receiver
// (receive side); one instance serves both, the instrument sets are disjoint.
type Metrics struct {
	// Deliveries counts outbound delivery outcomes, labels {result, source}.
	Deliveries kernelmetrics.CounterVec
	// DeliveryDuration observes outbound delivery wall-clock seconds, labels {source}.
	DeliveryDuration kernelmetrics.HistogramVec
	// SignatureFailures counts inbound signature-verification failures, labels {source, reason}.
	SignatureFailures kernelmetrics.CounterVec
	// IdempotencyHits counts inbound duplicate deliveries (claim already done /
	// in-flight), labels {source}.
	IdempotencyHits kernelmetrics.CounterVec
}

// errRegisterMetricFmt is the format string for metric registration errors.
// The two verbs are the metric name and the underlying error.
const errRegisterMetricFmt = "webhook: register %s: %w"

// RegisterMetrics registers the four webhook instruments on p with canonical
// names, labels, and buckets, returning them bundled. It is the single source
// for webhook metric identity; the bootstrap auto-wire calls it once and injects
// the result into the Dispatcher and the runtime Receiver.
func RegisterMetrics(p kernelmetrics.Provider) (Metrics, error) {
	deliveries, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricWebhookDeliveriesTotal,
		Help:       "Total outbound webhook deliveries by outcome.",
		LabelNames: []string{labelResult, labelSource},
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricWebhookDeliveriesTotal, err)
	}
	duration, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       metricWebhookDeliveryDuration,
		Help:       "Outbound webhook delivery wall-clock duration in seconds.",
		LabelNames: []string{labelSource},
		Buckets:    webhookDeliveryDurationBuckets,
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricWebhookDeliveryDuration, err)
	}
	sigFailures, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricWebhookSignatureFailures,
		Help:       "Total inbound webhook signature-verification failures by reason.",
		LabelNames: []string{labelSource, labelReason},
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricWebhookSignatureFailures, err)
	}
	idempotencyHits, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricWebhookIdempotencyHits,
		Help:       "Total inbound webhook duplicate deliveries deduplicated by the idempotency claimer.",
		LabelNames: []string{labelSource},
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricWebhookIdempotencyHits, err)
	}
	return Metrics{
		Deliveries:        deliveries,
		DeliveryDuration:  duration,
		SignatureFailures: sigFailures,
		IdempotencyHits:   idempotencyHits,
	}, nil
}

// preflight probes each non-nil instrument's With() with a representative label
// set, under recover. With() panics (MustValidateLabels) on a label-set
// mismatch; catching it here turns a misconfigured vec into a fail-fast wiring
// error instead of a crash on first record. The recovered value is redacted
// before wrapping so a credential-bearing panic string cannot leak. Mirrors
// reconcile.Metrics.preflight.
func (m Metrics) preflight() (err error) {
	defer func() {
		if r := recover(); r != nil {
			redacted := redaction.RedactError(fmt.Errorf("%v", r))
			err = fmt.Errorf("webhook: metrics label set invalid: %w", redacted)
		}
	}()
	if m.Deliveries != nil {
		_ = m.Deliveries.With(kernelmetrics.Labels{labelResult: string(deliverySuccess), labelSource: "_preflight"})
	}
	if m.DeliveryDuration != nil {
		_ = m.DeliveryDuration.With(kernelmetrics.Labels{labelSource: "_preflight"})
	}
	if m.SignatureFailures != nil {
		_ = m.SignatureFailures.With(kernelmetrics.Labels{labelSource: "_preflight", labelReason: string(ReasonBadSignature)})
	}
	if m.IdempotencyHits != nil {
		_ = m.IdempotencyHits.With(kernelmetrics.Labels{labelSource: "_preflight"})
	}
	return nil
}

// recordDelivery increments webhook_deliveries_total{result, source} when wired.
// The result parameter is the sealed webhookDeliveryResult type; the archtest
// callsite guard (WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01) additionally bans any
// caller passing a value that is not one of the declared delivery* consts.
func (m Metrics) recordDelivery(ctx context.Context, source string, result webhookDeliveryResult) {
	if m.Deliveries == nil {
		return
	}
	m.Deliveries.With(kernelmetrics.Labels{labelResult: string(result), labelSource: source}).Inc(ctx)
}

// observeDeliveryDuration records webhook_delivery_duration_seconds{source} when wired.
func (m Metrics) observeDeliveryDuration(ctx context.Context, source string, seconds float64) {
	if m.DeliveryDuration == nil {
		return
	}
	m.DeliveryDuration.With(kernelmetrics.Labels{labelSource: source}).Observe(ctx, seconds)
}

// RecordSignatureFailure increments webhook_signature_failures_total{source, reason}
// when wired. Called by the runtime Receiver after a verification failure with
// the typed reason its verify step classified.
func (m Metrics) RecordSignatureFailure(ctx context.Context, source string, reason SignatureFailureReason) {
	if m.SignatureFailures == nil {
		return
	}
	m.SignatureFailures.With(kernelmetrics.Labels{labelSource: source, labelReason: string(reason)}).Inc(ctx)
}

// RecordIdempotencyHit increments webhook_idempotency_hits_total{source} when
// wired. Called by the runtime Receiver when a delivery is a known duplicate
// (claim already done or in-flight).
func (m Metrics) RecordIdempotencyHit(ctx context.Context, source string) {
	if m.IdempotencyHits == nil {
		return
	}
	m.IdempotencyHits.With(kernelmetrics.Labels{labelSource: source}).Inc(ctx)
}
