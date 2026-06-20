package metrics

// protection_label.go — sealed ProtectionType for grpc_protection_rejected_total.
//
// INVARIANT: GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01
//
// ProtectionType is the SOLE source of the `type` label value for
// grpc_protection_rejected_total{type,method,cell}. The struct field is
// unexported, making inline construction outside this package a compile error
// (Hard upstream seal — "sealed construction" range per ai-robust.md §Hard 范本).
// The frozen value set {ratelimit, circuit} is enforced by two mechanisms:
//
//   - HARD (upstream): the unexported field prevents package-outside injection
//     of an arbitrary string value — only ProtectionRateLimit() and
//     ProtectionCircuit() produce valid ProtectionType instances.
//   - MEDIUM (archtest): GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01
//     in tools/archtest/grpc_protection_label_test.go enumerates the accessor
//     return values against an independent hardcoded golden and scans the
//     interceptor callsites for non-accessor ptype arguments.
//
// Business interceptors (rate_limit.go, circuit_breaker.go) call only these
// two accessors; they cannot construct a ProtectionType from an arbitrary string.

// ProtectionType is the sealed type for the `type` label on
// grpc_protection_rejected_total. It can only be constructed via the two
// package-level accessors ProtectionRateLimit and ProtectionCircuit — the
// unexported field v prevents external construction.
type ProtectionType struct {
	// v is the validated type value. Unexported: a ProtectionType cannot be
	// constructed outside this package, preventing arbitrary label injection.
	v string
}

// ProtectionRateLimit returns the ProtectionType for the "ratelimit" label value,
// representing a request rejected by the gRPC rate-limit interceptor
// (UnaryRateLimit / StreamRateLimit deny path).
//
// This is ONE of the TWO valid ProtectionType values for
// grpc_protection_rejected_total{type}. The complete closed set is
// {ratelimit, circuit} (frozen by GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01).
func ProtectionRateLimit() ProtectionType {
	return ProtectionType{v: "ratelimit"}
}

// ProtectionCircuit returns the ProtectionType for the "circuit" label value,
// representing a request rejected by the gRPC circuit-breaker interceptor
// (UnaryCircuitBreaker / StreamCircuitBreaker open path).
//
// This is ONE of the TWO valid ProtectionType values for
// grpc_protection_rejected_total{type}. The complete closed set is
// {ratelimit, circuit} (frozen by GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01).
func ProtectionCircuit() ProtectionType {
	return ProtectionType{v: "circuit"}
}

// String returns the metric label value for this ProtectionType. The zero
// value (ProtectionType{}) returns "" — callers should always use an accessor.
func (p ProtectionType) String() string {
	return p.v
}
