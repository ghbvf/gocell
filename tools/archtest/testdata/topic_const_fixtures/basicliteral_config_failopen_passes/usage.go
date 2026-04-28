// Package basicliteral_config_failopen_passes is a negative fixture:
// Topic is "config.value-changed.v1" (non-security prefix) with FailOpen.
// Rule must NOT fire because "config." is not in the security-sensitive prefix set.
package basicliteral_config_failopen_passes

import "fixturetest/outbox"

var _ = outbox.Entry{
	ID:            "x",
	Topic:         "config.value-changed.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
