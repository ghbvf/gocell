package interceptor

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/observability"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	runtimegrpc "github.com/ghbvf/gocell/framework/runtime/grpc"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// stream.go — gRPC streaming server interceptor chain (GAP-1 PR-10 [#1153]).
//
// The streaming chain is at full parity with the unary chain (chain.go) — same
// concerns in the same order, including Auth (shipping streaming without it would
// expose unauthenticated streams) — plus a stream-only StreamDrain. Each
// ctx-mutating interceptor forwards a context-wrapped stream (wrapped_stream.go)
// so the handler observes the enriched context. The cross-cutting cores
// (deriveRequestIDCtx / attributeCellCtx / startRPCSpan+finishRPCSpan / logAccess
// / authorize / recoverGRPCPanic) are shared with the unary interceptors — one
// source per concern across both transports. The metrics cell-label funnel is the
// sole exception: it is inlined per the GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01
// per-function provenance binding (see StreamMetrics).

// StreamRequestID is the streaming analog of UnaryRequestID — the OUTERMOST
// stream interceptor. It derives the request id into the stream context via the
// shared deriveRequestIDCtx core and forwards a context-wrapped stream.
func StreamRequestID() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, wrapServerStream(ss, deriveRequestIDCtx(ss.Context())))
	}
}

// StreamCellAttribution is the streaming analog of UnaryCellAttribution: it
// attributes the stream to its owning cell (writing kernel/ctxkeys.CellID) before
// the access-log and metrics interceptors read it.
func StreamCellAttribution(resolve CellResolver) grpc.StreamServerInterceptor {
	if resolve == nil {
		panic(panicregister.Approved("interceptor-cell-attribution-resolver-required",
			errcode.Assertion("interceptor.StreamCellAttribution: resolver is required")))
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, wrapServerStream(ss, attributeCellCtx(ss.Context(), resolve, info.FullMethod)))
	}
}

// StreamTracing is the streaming analog of UnaryTracing: the span spans the whole
// stream lifetime (opened at stream start, closed at stream end), recording the
// final status. A nil tracer degrades to NoopTracer.
func StreamTracing(tracer wrapper.Tracer) grpc.StreamServerInterceptor {
	if tracer == nil {
		tracer = wrapper.NoopTracer{}
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, span := startRPCSpan(ss.Context(), tracer, info.FullMethod)
		defer span.End()
		err := handler(srv, wrapServerStream(ss, ctx))
		finishRPCSpan(span, err)
		return err
	}
}

// StreamAccessLog is the streaming analog of UnaryAccessLog: it logs one
// structured slog.Info line per stream at close, with the HTTP-parity field set.
// clk is required (clock.MustHaveClock).
func StreamAccessLog(clk clock.Clock) grpc.StreamServerInterceptor {
	clock.MustHaveClock(clk, "interceptor.StreamAccessLog")
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := clk.Now()
		err := handler(srv, ss)
		logAccess(ss.Context(), clk, start, info.FullMethod, err)
		return err
	}
}

// StreamMetrics is the streaming analog of UnaryMetrics: it records
// grpc_server_requests_total / grpc_server_request_duration_seconds once per
// stream at close (duration = whole stream lifetime). Ops note: during a
// graceful shutdown the framework drain (StreamDrain) cancels in-flight streams,
// so they close with code=Canceled — a Canceled spike on this metric within a
// drain/deploy window is expected, not an outage. The cell-label funnel
// (metrics.ResolveCellLabel → GRPCCollector.RecordRPC) is INLINED here — not
// shared with UnaryMetrics — because GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 binds
// the funnel by go/types object identity WITHIN each metrics interceptor body;
// moving it to a shared helper would defeat that per-function provenance check.
//
// collector and clk are required (programmer-error panic on nil).
func StreamMetrics(collector metrics.GRPCCollector, clk clock.Clock, validCellIDs map[string]struct{}) grpc.StreamServerInterceptor {
	clock.MustHaveClock(clk, "interceptor.StreamMetrics")
	if validation.IsNilInterface(collector) {
		panic(panicregister.Approved("interceptor-metrics-collector-required",
			errcode.Assertion("interceptor.StreamMetrics: collector is required")))
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := clk.Now()
		err := handler(srv, ss)
		ctx := ss.Context()
		observability.SafeObserve(slog.Default(), func() {
			code := status.Code(err).String()
			cell := metrics.ResolveCellLabel(ctx, validCellIDs)
			collector.RecordRPC(ctx, cell, info.FullMethod, code, clk.Since(start).Seconds())
		})
		return err
	}
}

// StreamAuth is the streaming analog of UnaryAuth: it authenticates the stream at
// open via the shared authorize core (the single source of the gRPC bearer-auth
// decision across both transports) and forwards a principal-enriched,
// context-wrapped stream. The verifier is required (programmer-error panic on
// nil) — the server must refuse to start rather than expose unauthenticated
// streams.
//
// For owner-scoped methods (#2207) the resource lives in the request message,
// which is NOT available at stream open. The permission gate is therefore DEFERRED
// ENTIRELY to the first RecvMsg via resourceGatedStream — the coarse (fullMethod)
// gate MUST NOT run at open for these methods, because it would wrongly DENY the
// owner (subject == fullMethod never holds) before the per-message check ever runs.
// Coarse methods (no resource selector) keep the open-time gate. This is the
// streaming analog of UnaryAuth's extractResourceForUnary path (unary has the
// message at interceptor entry, so it gates once with the right resource).
func StreamAuth(verifier auth.IntentTokenVerifier, opts ...AuthOption) grpc.StreamServerInterceptor {
	if validation.IsNilInterface(verifier) {
		panic(panicregister.Approved("interceptor-auth-verifier-required",
			errcode.Assertion("interceptor.StreamAuth: verifier is required")))
	}
	cfg := authConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		authCtx, p, err := authorizeWithPrincipal(ss.Context(), cfg, verifier, info.FullMethod)
		if err != nil {
			return err
		}
		wrapped := wrapServerStream(ss, authCtx)
		// p == nil → public method (bypass granted by authorizeWithPrincipal); no
		// permission gate.
		if p == nil {
			return handler(srv, wrapped)
		}
		// Owner-scoped method (#2207): DEFER the whole permission gate to the first
		// RecvMsg, where the resource field value is known. Running the coarse gate
		// here would deny the owner. The deferred gate (resourceGatedStream.RecvMsg)
		// runs during handler execution, so a panic in it is collapsed to
		// codes.Internal by the innermost StreamRecovery interceptor (F3 fail-closed
		// on extraction failure is a returned deny, not a panic).
		if cfg.resourceFor != nil {
			if fieldName, hasField := cfg.resourceFor(info.FullMethod); hasField {
				return handler(srv, &resourceGatedStream{
					ServerStream: wrapped,
					ctx:          authCtx,
					cfg:          cfg,
					p:            p,
					fullMethod:   info.FullMethod,
					fieldName:    fieldName,
				})
			}
		}
		// Coarse method: gate at open with resource=fullMethod (pre-#2207 behavior).
		if err := gateStreamAtOpen(authCtx, cfg, p, info.FullMethod); err != nil {
			return err
		}
		return handler(srv, wrapped)
	}
}

// gateStreamAtOpen runs the coarse permission gate (resource = fullMethod) for a
// non-public, non-owner-scoped streaming method at stream open. It installs a
// stage-level panic guard mirroring authorizeWithPrincipal (#1790): the gate runs
// OUTSIDE StreamRecovery, so a panicking PDP/resolver is collapsed to codes.Internal
// here rather than escaping the chain. Owner-scoped methods do NOT use this — their
// gate is deferred to resourceGatedStream.RecvMsg (#2207).
func gateStreamAtOpen(ctx context.Context, cfg authConfig, p *auth.Principal, fullMethod string) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = recoverGRPCPanic(ctx, "auth", fullMethod, v)
		}
	}()
	return authorizePermission(ctx, cfg, p, fullMethod, fullMethod)
}

// StreamDrain binds each in-flight stream's handler context to the shared
// DrainSignal (PR-10 #1153): when the adapter triggers drain at GracefulStop
// start, the derived context is canceled, so a handler that selects on
// ctx.Done() returns promptly instead of blocking GracefulStop until the
// hard-stop deadline. It runs one bounded goroutine per active stream, freed when
// the stream ends (defer cancel → ctx.Done) or drain fires. drain is required
// (programmer-error panic on nil; adapters/grpc.New stores the SAME instance
// from Config.Interceptors for its graceful-stop trigger).
func StreamDrain(drain *runtimegrpc.DrainSignal) grpc.StreamServerInterceptor {
	if drain == nil {
		panic(panicregister.Approved("interceptor-stream-drain-required",
			errcode.Assertion("interceptor.StreamDrain: drain signal is required")))
	}
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, cancel := context.WithCancel(ss.Context())
		defer cancel()
		go func() {
			select {
			case <-drain.Context().Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		return handler(srv, wrapServerStream(ss, ctx))
	}
}

// StreamRecovery is the INNERMOST streaming interceptor: it recovers a panic from
// the handler, logs the redacted value via the shared recoverGRPCPanic core, and
// returns codes.Internal. It never re-panics — the panic is collapsed into a
// return value so the outer Metrics/Tracing interceptors observe a clean status
// (outside PANIC-REGISTERED-01, same as the unary path).
func StreamRecovery() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = recoverGRPCPanic(ss.Context(), "handler", info.FullMethod, v)
			}
		}()
		return handler(srv, ss)
	}
}

// newStreamChain composes the streaming interceptors into a single
// grpc.ServerOption — the streaming analog of newUnaryChain, at full parity
// (including auth) plus the stream-only StreamDrain. It is package-private:
// NewServerInterceptors is the sole caller and supplies the shared registrar +
// drain it mints (#1752). The order
//
//	RequestID → CellAttribution → Tracing → AccessLog → Metrics → Auth → Drain → Recovery
//
// (RequestID outermost, Recovery innermost, Drain just inside Auth so the handler
// context is drain-bound) is fixed here and guarded by GRPC-STREAM-CHAIN-ORDER-01.
// The grpc.ChainStreamInterceptor caller is locked to this file by
// GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01 (Medium downstream + Go-ceiling upstream).
// The single-wiring-object upgrade that forces BOTH chains and the adapter to
// observe one registrar/drain (#1752) is realized here + in NewServerInterceptors:
// the registrar/drain are no longer caller-supplied Deps fields but minted by the
// funnel, so a mismatch is unrepresentable (GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01).
//
// reg, CellIDClosedSet, and drain are required (fail-closed): a chain composed
// without them would silently relabel every RPC to the runtime sentinel or leave
// streams un-drainable.
func newStreamChain(deps Deps, reg *runtimegrpc.ServiceRegistrar, drain *runtimegrpc.DrainSignal) grpc.ServerOption {
	if reg == nil {
		panic(panicregister.Approved("interceptor-chain-registrar-required",
			errcode.Assertion("interceptor.newStreamChain: registrar is required")))
	}
	if len(deps.CellIDClosedSet) == 0 {
		panic(panicregister.Approved("interceptor-chain-cell-closed-set-required",
			errcode.Assertion(
				"interceptor.newStreamChain: Deps.CellIDClosedSet is required (the assembly cell-id set)")))
	}
	if drain.Validate() != nil {
		panic(panicregister.Approved("interceptor-chain-drain-required",
			errcode.Assertion("interceptor.newStreamChain: drain is required and must be "+
				"built with runtimegrpc.NewDrainSignal() (a nil or zero-value DrainSignal would "+
				"panic at GracefulStop)")))
	}
	validCellIDs := buildValidCellIDs(deps.CellIDClosedSet)
	return grpc.ChainStreamInterceptor(
		StreamRequestID(),
		StreamCellAttribution(reg.CellIDForMethod),
		StreamTracing(deps.Tracer),
		StreamAccessLog(deps.Clock),
		StreamMetrics(deps.Collector, deps.Clock, validCellIDs),
		// Registrar-sourced public-method bypass (#1675) + per-method permission gate
		// (#2008), same single-source wiring as the unary chain — see authChainOptions
		// in chain.go.
		StreamAuth(deps.Verifier, authChainOptions(deps, reg)...),
		StreamDrain(drain),
		StreamRecovery(),
	)
}
