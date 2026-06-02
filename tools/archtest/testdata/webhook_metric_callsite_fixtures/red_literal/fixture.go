//go:build archtest_fixture

// Package recorddeliveryred is a RED fixture for the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 callsite guard: recordDelivery called
// with an inline string literal (the residual hole the sealed
// webhookDeliveryResult type cannot close — an untyped string constant is
// assignable to a defined string type) must be flagged.
package recorddeliveryred

import "context"

type webhookDeliveryResult string

const deliverySuccess webhookDeliveryResult = "success"

// Metrics mirrors kernel/webhook.Metrics' recordDelivery method shape.
type Metrics struct{}

func (m Metrics) recordDelivery(_ context.Context, _ string, _ webhookDeliveryResult) {}

func caller(ctx context.Context, m Metrics) {
	m.recordDelivery(ctx, "stripe", "rogue") // inline literal — MUST be flagged
	_ = deliverySuccess
}
