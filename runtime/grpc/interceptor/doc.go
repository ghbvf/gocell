// Package interceptor provides the gRPC unary and streaming server interceptor
// chains that align the gRPC transport with the existing HTTP middleware stack
// (runtime/http/middleware): RequestID, CellAttribution, Tracing, AccessLog,
// Metrics, Auth, and Recovery.
//
// The unary interceptors are composed by NewUnaryChain and the streaming
// interceptors by NewStreamChain, each into a single grpc.ServerOption.
// bootstrap installs both options on the adapters/grpc server; this package owns
// no server lifecycle. The cross-cutting cores (request-id derivation, cell
// attribution, span open/close, access logging, the bearer-auth decision, and
// panic collapse) are shared between the unary and streaming variants — one
// source per concern across both transports. The metrics cell-label funnel is
// the sole intentional exception: it is inlined in each metrics interceptor body
// per the GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 per-function provenance binding.
//
// # Chain order
//
// NewUnaryChain composes the interceptors in this fixed order (outermost →
// innermost), where the first argument to grpc.ChainUnaryInterceptor is the
// outermost wrapper closest to the transport:
//
//	RequestID → CellAttribution → Tracing → AccessLog → Metrics → Auth → Recovery → handler
//
// NewStreamChain composes the streaming analogs in the same order plus a
// stream-only StreamDrain just inside Auth (so the handler's context is bound to
// the framework drain signal, PR-10 #1153):
//
//	RequestID → CellAttribution → Tracing → AccessLog → Metrics → Auth → Drain → Recovery → handler
//
// This mirrors the HTTP listener-root order (CellAttribution → Tracing →
// AccessLog → Metrics). CellAttribution runs before every interceptor that
// reads the cell label (AccessLog, Metrics) so the owning cell is in ctx when
// they observe it; AccessLog runs after Tracing (so a propagated trace_id is in
// ctx) and OUTER to Auth (so auth rejections are still logged). StreamDrain sits
// just inside Auth so the drain-bound, principal-enriched context reaches the
// handler while the outer observability interceptors still see the final status
// (e.g. codes.Canceled when a drain cuts a stream short).
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
