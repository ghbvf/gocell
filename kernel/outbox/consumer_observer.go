package outbox

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// ConsumerObserver receives notifications from ConsumerBase when a delivery
// reaches a terminal Reject disposition. Implementations are provided by
// runtime/observability/metrics (OutboxConsumerCollector) so kernel/outbox
// stays free of metric backend imports.
//
// ObserveReject is called exactly once per terminal Reject (handler-explicit
// reject OR retry-budget exhaustion); cellID / topic / consumerGroup are the
// observability dimensions, reason is a closed-set string drawn from
// ConsumerRejectReason* constants.
//
// Implementers should NOT include consumerGroup in metric label sets — it is
// provided here for slog/tracing dimensions but adding it as a Prometheus label
// expands cardinality multiplicatively (every consumer group × topic × cell ×
// reason combination becomes a unique time-series). See
// runtime/observability/metrics.OutboxConsumerCollector for the sanctioned label
// set {cell, topic, reason}.
//
// ref: kernel/outbox/relay_collector.go — RelayCollector is the analogous
// interface for the Relay path; same kernel-defined-runtime-implemented
// pattern keeps adapter-backend imports out of kernel.
type ConsumerObserver interface {
	ObserveReject(cellID, topic, consumerGroup, reason string)
}

// ConsumerRejectReason* are the closed-set values passed to
// ConsumerObserver.ObserveReject. Adding a new reason requires touching
// every observer implementation (cross-PR conformance) — keep the set
// small and well-defined.
const (
	// ConsumerRejectReasonHandlerReject indicates the business handler
	// explicitly returned DispositionReject (permanent failure / DLX).
	ConsumerRejectReasonHandlerReject = "handler_reject"

	// ConsumerRejectReasonRetryExhausted indicates the retry budget was
	// exhausted without the handler returning Ack or Reject. ConsumerBase
	// converts to DispositionReject so the broker routes to DLX.
	ConsumerRejectReasonRetryExhausted = "retry_exhausted"
)

// ErrObserverAlreadyAttached is returned by ConsumerBase.AttachObserver when
// an observer has already been attached. Re-attaching would silently swap
// the in-use observer and lose previous emissions; the wiring-fail-fast
// pattern (runtime-api.md §Option 范式分层) requires it surface as an error.
var ErrObserverAlreadyAttached = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
	"outbox: ConsumerObserver already attached; AttachObserver may only be called once per ConsumerBase")

// NopConsumerObserver is the default ConsumerObserver used when no metrics
// provider is wired. ObserveReject is a no-op.
type NopConsumerObserver struct{}

// ObserveReject is a no-op: NopConsumerObserver is the default observer when no
// metrics collector is wired. Discarding the observation is deliberate — the
// no-op exists so ConsumerBase can always call ObserveReject unconditionally
// without a nil guard.
func (NopConsumerObserver) ObserveReject(_, _, _, _ string) {}

// compile-time interface check — fail at build if ConsumerObserver shape changes.
var _ ConsumerObserver = NopConsumerObserver{}

// isNilObserver returns true if o is nil or a typed-nil wrapped in
// the interface. Uses the shared pkg/validation helper to keep typed-nil
// detection single-source (ERROR-FIRST-TYPED-NIL-01).
func isNilObserver(o ConsumerObserver) bool {
	return validation.IsNilInterface(o)
}
