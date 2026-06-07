//go:build archtest_fixture

// Package redclassifierliteral is a RED fixture for the
// MQTT-METRIC-LABEL-VALUES-FROZEN-01 reason provenance backstop: a classifier
// function whose result type is a reason enum returns an inline string literal
// (implicitly converted to the enum) — the F1 hole the XReason(...) conversion ban
// and the Record* callsite guard both miss — must be flagged.
package redclassifierliteral

type ConsumeFailureReason string

func classify() ConsumeFailureReason {
	return "rogue" // off-set literal implicitly converted at return — MUST be flagged
}
