//go:build archtest_fixture

// Package recordsignaturefailurereasonliteral is a RED fixture for the
// WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 RecordSignatureFailure callsite guard: an
// inline string literal passed as the reason. This is the F2′ hole — the reason
// side previously had NO callsite guard, and a bare string literal implicitly
// converts to SignatureFailureReason (so the conversion-ban scanner, which only
// matches an explicit SignatureFailureReason(...) CallExpr, never saw it). The new
// callsite guard must flag this.
package recordsignaturefailurereasonliteral

import "context"

type SignatureFailureReason string

const ReasonBadSignature SignatureFailureReason = "bad_signature"

// Metrics mirrors kernel/webhook.Metrics' RecordSignatureFailure method shape.
type Metrics struct{}

func (m Metrics) RecordSignatureFailure(_ context.Context, _ string, _ SignatureFailureReason) {}

func caller(ctx context.Context, m Metrics) {
	m.RecordSignatureFailure(ctx, "stripe", "rogue") // inline literal — MUST be flagged
	_ = ReasonBadSignature
}
