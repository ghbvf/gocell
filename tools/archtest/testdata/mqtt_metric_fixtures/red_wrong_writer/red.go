//go:build archtest_fixture

// Package redwrongwriter is a RED fixture for the
// MQTT-METRIC-LABEL-VALUES-FROZEN-01 sole-writer lock receiver-identity check
// (F2): a FREE FUNCTION named AdjustInflight — the name matches the sanctioned
// writer set, but it is not a method on a provider collector — writes an
// instrument directly. The old name-only check skipped it; the receiver-identity
// check must flag it.
package redwrongwriter

import "context"

type counterVec struct{}

func (counterVec) With(map[string]string) counter { return counter{} }

type counter struct{}

func (counter) Inc(context.Context) {}

type providerSubscriberCollector struct {
	consumeFailed counterVec
}

// AdjustInflight is a FREE FUNCTION (no receiver) — name collides with a sanctioned
// writer but it is not a provider-collector method, so it must be flagged.
func AdjustInflight(c *providerSubscriberCollector, ctx context.Context) {
	c.consumeFailed.With(map[string]string{"reason": "x"}).Inc(ctx)
}
