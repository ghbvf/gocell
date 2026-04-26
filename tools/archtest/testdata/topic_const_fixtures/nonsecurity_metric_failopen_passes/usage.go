// Package nonsecurity_metric_failopen_passes is a negative fixture:
// Topic is a non-security topic ("metric.recorded.v1") with FailOpen.
// Rule must NOT fire because the topic prefix is not in the security set.
package nonsecurity_metric_failopen_passes

import "fixturetest/outbox"

const metricTopic = "metric.recorded.v1"

var _ = outbox.Entry{
	ID:            "x",
	Topic:         metricTopic,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
