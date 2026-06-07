//go:build archtest_fixture

// Package redtypedvar is a RED fixture for the
// MQTT-METRIC-LABEL-VALUES-FROZEN-01 reason provenance backstop: a typed var of a
// reason enum initialized from an inline string literal (implicit conversion at
// var init — the "typed var 漂移" vector) must be flagged. Only `const` declaration
// values are exempt; `var` initializers are not.
package redtypedvar

type PublishFailureReason string

var leaked PublishFailureReason = "rogue" // off-set var init — MUST be flagged
