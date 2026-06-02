//go:build archtest_fixture

// Package recorddeliverygreen is the GREEN over-fire guard for the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 callsite guard: a declared const and a
// typed (non-constant) webhookDeliveryResult value are both allowed — only
// inline constants are banned.
package recorddeliverygreen

import "context"

type webhookDeliveryResult string

const deliverySuccess webhookDeliveryResult = "success"

// Metrics mirrors kernel/webhook.Metrics' recordDelivery method shape.
type Metrics struct{}

func (m Metrics) recordDelivery(_ context.Context, _ string, _ webhookDeliveryResult) {}

func classify() webhookDeliveryResult { return deliverySuccess }

func caller(ctx context.Context, m Metrics, result webhookDeliveryResult) {
	m.recordDelivery(ctx, "stripe", deliverySuccess) // named const — allowed
	m.recordDelivery(ctx, "stripe", result)          // typed (non-constant) var — allowed
	m.recordDelivery(ctx, "stripe", classify())      // typed (non-constant) call — allowed
}
