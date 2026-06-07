//go:build archtest_fixture

// Package redliteral is a RED fixture for the MQTT-METRIC-LABEL-VALUES-FROZEN-01
// callsite guard: a Record* method called with an inline string literal reason
// (the residual hole the sealed reason type cannot close — an untyped string
// constant is assignable to a defined string type) must be flagged.
package redliteral

import "context"

type ConsumeFailureReason string

type providerSubscriberCollector struct{}

func (c *providerSubscriberCollector) RecordConsumeFailure(_ context.Context, _ ConsumeFailureReason) {}

func emit(c *providerSubscriberCollector, ctx context.Context) {
	c.RecordConsumeFailure(ctx, "rogue") // inline literal — MUST be flagged
}
