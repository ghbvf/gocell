//go:build archtest_fixture

// Package recorddeliveryuntypedconst is a RED fixture for the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 recordDelivery callsite guard: an UNTYPED
// string constant assigned through to the sealed webhookDeliveryResult parameter.
// This is the F2 hole the prior guard let through — it accepted ANY *types.Const,
// not just a declared delivery* const, so `const x = "rogue"; recordDelivery(x)`
// implicitly converted and bypassed the freeze. The tightened guard checks the
// const's TYPE is identical to the enum type and must flag this.
package recorddeliveryuntypedconst

import "context"

type webhookDeliveryResult string

const deliverySuccess webhookDeliveryResult = "success"

// rogue is an UNTYPED string constant — its object type is untyped string, NOT
// webhookDeliveryResult, so it must not be accepted as a frozen label value.
const rogue = "rogue"

// Metrics mirrors kernel/webhook.Metrics' recordDelivery method shape.
type Metrics struct{}

func (m Metrics) recordDelivery(_ context.Context, _ string, _ webhookDeliveryResult) {}

func caller(ctx context.Context, m Metrics) {
	m.recordDelivery(ctx, "stripe", rogue) // untyped const — MUST be flagged
	_ = deliverySuccess
}
