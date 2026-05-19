package metrics

// outbox_split_stubs.go — Wave 1 compile stubs for the P1#2/#3 collector split.
//
// These stubs let the RED tests compile and FAIL semantically.
// Wave 2 will DELETE this file and replace with real implementations in outbox.go.
//
// Stub strategy (per plan §Constraints option (a)):
//   - OutboxRejectCollector wraps OutboxConsumerCollector("_stub") so it registers
//     BOTH metrics — causing the "does NOT register outbox_pending_depth" RED test
//     to fail as expected.
//   - OutboxPendingDepthCollector wraps OutboxConsumerCollector("_stub") so the
//     cell label is "_stub", NOT the constructed cellID — causing the cell-label
//     RED test to fail as expected.

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// OutboxRejectCollector stub — Wave 1 only.
// Real implementation: implements outbox.ConsumerObserver, registers ONLY
// outbox_consumer_rejected_total, no cellID constructor arg.
//
// Current stub deliberately wraps OutboxConsumerCollector to make RED tests fail.
type OutboxRejectCollector struct {
	inner *OutboxConsumerCollector
}

// compile-time interface check.
var _ outbox.ConsumerObserver = (*OutboxRejectCollector)(nil)

// NewOutboxRejectCollector is the stub constructor.
// Wave 2 target: func NewOutboxRejectCollector(p kernelmetrics.Provider) (*OutboxRejectCollector, error)
func NewOutboxRejectCollector(p kernelmetrics.Provider) (*OutboxRejectCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: OutboxRejectCollector Provider is required")
	}
	// Stub: delegates to OutboxConsumerCollector which registers BOTH metrics.
	// Wave 1 RED: TestOutboxRejectCollector_RegistersOnlyRejectedCounter FAILS
	// because outbox_pending_depth is registered by the inner collector.
	inner, err := NewOutboxConsumerCollector(p, "_stub")
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: OutboxRejectCollector stub: %w", err)
	}
	return &OutboxRejectCollector{inner: inner}, nil
}

// ObserveReject forwards to the inner collector.
func (c *OutboxRejectCollector) ObserveReject(cellID, topic, consumerGroup, reason string) {
	if c == nil {
		return
	}
	c.inner.ObserveReject(cellID, topic, consumerGroup, reason)
}

// ---------------------------------------------------------------------------

// OutboxPendingDepthCollector stub — Wave 1 only.
// Real implementation: implements runtimeoutbox.PendingDepthObserver, registers
// ONLY outbox_pending_depth, cellID required at construction.
//
// Current stub deliberately wraps OutboxConsumerCollector("_stub") so the cell
// label is "_stub" rather than the supplied cellID — making RED tests fail.
type OutboxPendingDepthCollector struct {
	inner *OutboxConsumerCollector
}

// compile-time interface check.
var _ runtimeoutbox.PendingDepthObserver = (*OutboxPendingDepthCollector)(nil)

// NewOutboxPendingDepthCollector is the stub constructor.
// Wave 2 target: uses supplied cellID; empty cellID returns error.
func NewOutboxPendingDepthCollector(p kernelmetrics.Provider, cellID string) (*OutboxPendingDepthCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: OutboxPendingDepthCollector Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: OutboxPendingDepthCollector cellID is required")
	}
	// Stub: delegates to OutboxConsumerCollector("_stub"), ignores the supplied
	// cellID — so cell label will be "_stub", not the actual cellID.
	// Wave 1 RED: TestOutboxPendingDepthCollector_ObservePendingDepth_UsesConstructedCellID
	// FAILS because the label is "_stub", not "configcore".
	// Wave 1 RED: TestOutboxPendingDepthCollector_RegistersOnlyPendingDepthGauge
	// FAILS because outbox_consumer_rejected_total is also registered by the stub.
	inner, err := NewOutboxConsumerCollector(p, "_stub")
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: OutboxPendingDepthCollector stub: %w", err)
	}
	return &OutboxPendingDepthCollector{inner: inner}, nil
}

// ObservePendingDepth forwards to the inner collector.
func (c *OutboxPendingDepthCollector) ObservePendingDepth(n int64) {
	if c == nil {
		return
	}
	c.inner.ObservePendingDepth(n)
}
