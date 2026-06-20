//go:build archtest_fixture

// Package grpcprotectionred is the RED fixture for
// GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 callsite guard: a
// RecordProtectionRejection call whose ptype argument is NOT a direct call to a
// ProtectionType accessor (ProtectionRateLimit() / ProtectionCircuit()) must be
// flagged. This fixture passes a variable — even though the variable holds an
// accessor return value, the A2 scan requires the call-site argument to be a
// direct accessor call expression (blind spot documented in the test file).
// Expect 1 diagnostic.
package grpcprotectionred

import (
	"context"

	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// GRPCCollector mirrors the GRPCCollector interface shape for the scanner.
// The scanner matches on the method name "RecordProtectionRejection"; the
// concrete type here is irrelevant to the scan (it uses TypesInfo.Selections).
type GRPCCollector struct{}

func (c GRPCCollector) RecordProtectionRejection(_ context.Context, _ metrics.CellLabel, _ string, _ metrics.ProtectionType) {
}

func caller(ctx context.Context, c GRPCCollector) {
	ptype := metrics.ProtectionRateLimit() // accessor return stored in var
	// ptype is not a direct accessor call at the call site — MUST be flagged.
	c.RecordProtectionRejection(ctx, metrics.CellLabel{}, "/pkg.Svc/Method", ptype)
}
