//go:build archtest_fixture

// Package green is the GREEN baseline for the MQTT-METRIC-LABEL-VALUES-FROZEN-01
// reason funnel: a declared-const callsite, no reason-type conversion, and an
// instrument .With(...) written only inside a sanctioned Record* method — all
// three guards report zero.
package green

import "context"

// ConsumeFailureReason mirrors the production sealed reason enum.
type ConsumeFailureReason string

const reasonReject ConsumeFailureReason = "reject"

type counterVec struct{}

func (counterVec) With(map[string]string) counter { return counter{} }

type counter struct{}

func (counter) Inc(context.Context) {}

// providerSubscriberCollector mirrors the production collector holding an
// unexported instrument field.
type providerSubscriberCollector struct {
	consumeFailed counterVec
}

// RecordConsumeFailure is a sanctioned writer — instrument .With(...) is allowed.
func (c *providerSubscriberCollector) RecordConsumeFailure(ctx context.Context, reason ConsumeFailureReason) {
	c.consumeFailed.With(map[string]string{"reason": string(reason)}).Inc(ctx)
}

// emit passes a declared const — allowed by the callsite guard.
func emit(c *providerSubscriberCollector, ctx context.Context) {
	c.RecordConsumeFailure(ctx, reasonReject)
}

// classify returns a declared const — allowed by the provenance backstop.
func classify() ConsumeFailureReason { return reasonReject }

// noFail returns the empty zero-value sentinel — allowed by the provenance backstop.
func noFail() (ConsumeFailureReason, bool) { return "", true }
