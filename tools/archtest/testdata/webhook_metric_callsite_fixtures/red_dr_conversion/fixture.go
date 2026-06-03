//go:build archtest_fixture

// Package recorddeliveryconversion is a RED fixture for the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 delivery-side conversion ban: a
// webhookDeliveryResult("x") conversion mints a value outside the declared
// delivery* const set. Banning the conversion is what makes the callsite guard's
// "typed non-constant value is allowed" branch safe (the trusted classifiers
// return declared consts, never conversions); a value laundered through a var or
// helper would otherwise slip past the callsite guard. The conversion scanner
// must flag this.
package recorddeliveryconversion

type webhookDeliveryResult string

const deliverySuccess webhookDeliveryResult = "success"

func sink(_ webhookDeliveryResult) {}

func caller() {
	sink(webhookDeliveryResult("rogue")) // conversion — MUST be flagged
	_ = deliverySuccess
}
