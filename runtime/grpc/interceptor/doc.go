// Package interceptor provides the gRPC unary server interceptor chain that
// aligns the gRPC transport with the existing HTTP middleware stack
// (runtime/http/middleware): RequestID, CellAttribution, Tracing, AccessLog,
// Metrics, Auth, and Recovery.
//
// The interceptors are composed by NewUnaryChain into a single
// grpc.ServerOption. bootstrap installs that option on the adapters/grpc
// server; this package owns no server lifecycle.
//
// # Chain order
//
// NewUnaryChain composes the interceptors in this fixed order (outermost →
// innermost), where the first argument to grpc.ChainUnaryInterceptor is the
// outermost wrapper closest to the transport:
//
//	RequestID → CellAttribution → Tracing → AccessLog → Metrics → Auth → Recovery → handler
//
// This mirrors the HTTP listener-root order (CellAttribution → Tracing →
// AccessLog → Metrics). CellAttribution runs before every interceptor that
// reads the cell label (AccessLog, Metrics) so the owning cell is in ctx when
// they observe it; AccessLog runs after Tracing (so a propagated trace_id is in
// ctx) and OUTER to Auth (so auth rejections are still logged).
//
// Recovery is INNERMOST, not outermost. This deliberately diverges from the
// HTTP middleware order (where Recovery sits inside Tracing/Metrics but its
// placement is driven by the HTTP ResponseWriter commit-ordering constraint —
// Recovery must own the writer to emit a 500 body). gRPC has no such
// constraint: a status code is a return value, so a panic recovered by the
// innermost interceptor and converted to codes.Internal propagates outward as
// a normal (resp, err) return that the outer Metrics and Tracing interceptors
// observe accurately. Placing Recovery innermost is the grpc-ecosystem
// go-grpc-middleware consensus, which documents recovery-last so metrics and
// logging see the converted status.
//
// ref: grpc-ecosystem/go-grpc-middleware interceptors/recovery/interceptors.go
// ref: go-kratos/kratos transport/grpc/interceptor.go
package interceptor
