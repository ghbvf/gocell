//go:build archtest_fixture

// Package reddirectwrite is a RED fixture for the
// MQTT-METRIC-LABEL-VALUES-FROZEN-01 sole-writer lock: an instrument .With(...)
// written from a NON-sanctioned function (bypassing the Record* funnel) must be
// flagged.
package reddirectwrite

import "context"

type counterVec struct{}

func (counterVec) With(map[string]string) counter { return counter{} }

type counter struct{}

func (counter) Inc(context.Context) {}

type providerSubscriberCollector struct {
	consumeFailed counterVec
}

// leak is NOT a sanctioned writer — writing the instrument here must be flagged.
func leak(c *providerSubscriberCollector, ctx context.Context) {
	c.consumeFailed.With(map[string]string{"reason": "x"}).Inc(ctx)
}
