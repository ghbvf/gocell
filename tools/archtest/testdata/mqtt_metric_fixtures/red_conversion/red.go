//go:build archtest_fixture

// Package redconversion is a RED fixture for the
// MQTT-METRIC-LABEL-VALUES-FROZEN-01 conversion ban: a XReason(...) conversion is
// the only way to mint an off-set reason value, so it must be flagged in
// production code.
package redconversion

type ConsumeFailureReason string

func leak() ConsumeFailureReason {
	return ConsumeFailureReason("rogue") // conversion — MUST be flagged
}
