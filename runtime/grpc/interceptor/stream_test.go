package interceptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	pkgctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// fakeServerStream is a minimal grpc.ServerStream for interceptor unit tests. It
// records sent messages and reports a configurable context.
type fakeServerStream struct {
	ctx  context.Context
	sent []any
}

func (f *fakeServerStream) Context() context.Context     { return f.ctx }
func (f *fakeServerStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeServerStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeServerStream) SetTrailer(metadata.MD)       {}
func (f *fakeServerStream) SendMsg(m any) error          { f.sent = append(f.sent, m); return nil }
func (f *fakeServerStream) RecvMsg(any) error            { return nil }

const streamMethod = "/pkg.Svc/Watch"

func streamInfo() *grpc.StreamServerInfo {
	return &grpc.StreamServerInfo{FullMethod: streamMethod, IsServerStream: true}
}

func TestStreamRequestID_GeneratesAndPropagates(t *testing.T) {
	var gotReqID, gotCorrID string
	handler := func(_ any, ss grpc.ServerStream) error {
		gotReqID, _ = pkgctxkeys.RequestIDFrom(ss.Context())
		gotCorrID, _ = pkgctxkeys.CorrelationIDFrom(ss.Context())
		return nil
	}
	ss := &fakeServerStream{ctx: context.Background()}
	if err := StreamRequestID()(nil, ss, streamInfo(), handler); err != nil {
		t.Fatalf("StreamRequestID: %v", err)
	}
	if gotReqID == "" {
		t.Fatalf("StreamRequestID must generate a request id into the stream context")
	}
	if gotCorrID != gotReqID {
		t.Fatalf("correlation id %q must equal request id %q", gotCorrID, gotReqID)
	}
}

func TestStreamRequestID_ReusesIncoming(t *testing.T) {
	const incoming = "req-abc-123"
	md := metadata.Pairs("x-request-id", incoming)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	var got string
	handler := func(_ any, ss grpc.ServerStream) error {
		got, _ = pkgctxkeys.RequestIDFrom(ss.Context())
		return nil
	}
	ss := &fakeServerStream{ctx: ctx}
	_ = StreamRequestID()(nil, ss, streamInfo(), handler)
	if got != incoming {
		t.Fatalf("StreamRequestID must reuse a valid incoming x-request-id, got %q want %q", got, incoming)
	}
}

func TestStreamCellAttribution_WritesCellID(t *testing.T) {
	resolve := func(string) (string, bool) { return "mycell", true }
	var got string
	handler := func(_ any, ss grpc.ServerStream) error {
		got, _ = kernelctxkeys.CellIDFrom(ss.Context())
		return nil
	}
	ss := &fakeServerStream{ctx: context.Background()}
	_ = StreamCellAttribution(resolve)(nil, ss, streamInfo(), handler)
	if got != "mycell" {
		t.Fatalf("StreamCellAttribution must write the resolved cell id, got %q", got)
	}
}

func TestStreamCellAttribution_NilResolverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("StreamCellAttribution with a nil resolver must panic")
		}
	}()
	_ = StreamCellAttribution(nil)
}

func TestStreamAuth_RejectsMissingToken(t *testing.T) {
	handlerReached := false
	handler := func(any, grpc.ServerStream) error { handlerReached = true; return nil }
	ss := &fakeServerStream{ctx: context.Background()}
	err := StreamAuth(stubVerifier{})(nil, ss, streamInfo(), handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("StreamAuth without a token must return Unauthenticated, got %v", status.Code(err))
	}
	if handlerReached {
		t.Fatalf("handler must not be reached when auth rejects the stream")
	}
}

func TestStreamAuth_PublicMethodBypasses(t *testing.T) {
	handlerReached := false
	handler := func(any, grpc.ServerStream) error { handlerReached = true; return nil }
	ss := &fakeServerStream{ctx: context.Background()}
	err := StreamAuth(stubVerifier{}, WithPublicMethod(func(m string) bool { return m == streamMethod }))(
		nil, ss, streamInfo(), handler)
	if err != nil {
		t.Fatalf("public method must bypass auth, got %v", err)
	}
	if !handlerReached {
		t.Fatalf("handler must be reached for a public method")
	}
}

func TestStreamAuth_NilVerifierPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("StreamAuth with a nil verifier must panic at construction")
		}
	}()
	_ = StreamAuth(nil)
}

func TestStreamMetrics_RecordsRPC(t *testing.T) {
	coll := metrics.NewInMemoryGRPCCollector()
	handler := func(any, grpc.ServerStream) error { return status.Error(codes.NotFound, "x") }
	ss := &fakeServerStream{ctx: kernelctxkeys.WithCellID(context.Background(), "mycell")}
	err := StreamMetrics(coll, clock.Real(), testCellSet("mycell"))(nil, ss, streamInfo(), handler)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("StreamMetrics must pass the handler error through, got %v", status.Code(err))
	}
	if got := coll.Count("mycell", streamMethod, codes.NotFound.String()); got != 1 {
		t.Fatalf("StreamMetrics count[mycell] = %d, want 1", got)
	}
}

func TestStreamMetrics_NilCollectorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("StreamMetrics with a nil collector must panic at construction")
		}
	}()
	_ = StreamMetrics(nil, clock.Real(), testCellSet("mycell"))
}

func TestStreamTracing_SpansStreamAndRecordsError(t *testing.T) {
	tr := &recordingTracer{span: &recordingSpan{}}
	handler := func(any, grpc.ServerStream) error { return status.Error(codes.NotFound, "x") }
	ss := &fakeServerStream{ctx: context.Background()}
	_ = StreamTracing(tr)(nil, ss, streamInfo(), handler)
	if !tr.span.ended {
		t.Fatalf("StreamTracing must end the span")
	}
	if tr.span.recordedErr == nil {
		t.Fatalf("StreamTracing must record the stream error")
	}
	if v, ok := tr.span.attr("rpc.system"); !ok || v != "grpc" {
		t.Fatalf("StreamTracing must set rpc.system=grpc, got %v ok=%v", v, ok)
	}
}

func TestStreamRecovery_ConvertsPanicToInternal(t *testing.T) {
	handler := func(any, grpc.ServerStream) error { panic("boom") }
	ss := &fakeServerStream{ctx: context.Background()}
	err := StreamRecovery()(nil, ss, streamInfo(), handler)
	if status.Code(err) != codes.Internal {
		t.Fatalf("StreamRecovery must convert a panic to codes.Internal, got %v", status.Code(err))
	}
}

func TestStreamDrain_CancelsHandlerCtxOnTrigger(t *testing.T) {
	drain := runtimegrpc.NewDrainSignal()
	started := make(chan struct{})
	handler := func(_ any, ss grpc.ServerStream) error {
		close(started)
		<-ss.Context().Done() // block until the drain cancels the stream ctx
		return ss.Context().Err()
	}
	ss := &fakeServerStream{ctx: context.Background()}
	errCh := make(chan error, 1)
	go func() { errCh <- StreamDrain(drain)(nil, ss, streamInfo(), handler) }()

	<-started
	drain.Trigger()

	select {
	case err := <-errCh:
		// The handler was blocked on ss.Context().Done() and returned only
		// because the drain canceled its context — it returns ctx.Err()
		// (context.Canceled). (gRPC maps that to codes.Canceled at the transport
		// layer; here we assert the raw cancellation the framework delivered.)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("drained stream handler must observe context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("StreamDrain did not cancel the handler context after Trigger")
	}
}

func TestStreamDrain_NoCancelOnNormalCompletion(t *testing.T) {
	drain := runtimegrpc.NewDrainSignal()
	handler := func(_ any, ss grpc.ServerStream) error {
		if ss.Context().Err() != nil {
			t.Errorf("stream ctx must not be canceled before drain fires")
		}
		return nil
	}
	ss := &fakeServerStream{ctx: context.Background()}
	if err := StreamDrain(drain)(nil, ss, streamInfo(), handler); err != nil {
		t.Fatalf("StreamDrain normal completion: %v", err)
	}
}

func TestStreamDrain_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("StreamDrain with a nil DrainSignal must panic at construction")
		}
	}()
	_ = StreamDrain(nil)
}

func streamDeps() Deps {
	return Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		Registrar:       runtimegrpc.NewServiceRegistrar(),
		CellIDClosedSet: []string{"svc-cell"},
		Drain:           runtimegrpc.NewDrainSignal(),
	}
}

func TestNewStreamChain_Smoke(t *testing.T) {
	opt := NewStreamChain(streamDeps())
	if opt == nil {
		t.Fatalf("NewStreamChain returned nil ServerOption")
	}
	_ = grpc.NewServer(opt) // must be installable without panicking
}

func TestNewStreamChain_NilDrainPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewStreamChain with a nil Deps.Drain must panic (streams would be un-drainable)")
		}
	}()
	d := streamDeps()
	d.Drain = nil
	_ = NewStreamChain(d)
}

func TestNewStreamChain_NilRegistrarPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewStreamChain with a nil Deps.Registrar must panic")
		}
	}()
	d := streamDeps()
	d.Registrar = nil
	_ = NewStreamChain(d)
}
