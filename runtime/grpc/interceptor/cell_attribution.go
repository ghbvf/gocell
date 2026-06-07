package interceptor

import (
	"context"

	"google.golang.org/grpc"

	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// CellResolver maps a gRPC fullMethod (e.g. "/pkg.Svc/Method") to the cell that
// owns it. The bool is false for an unregistered method (e.g. the native
// grpc-health service), so the cell label downstream degrades to the runtime
// sentinel. The composition root supplies the registrar's CellIDForMethod, which
// is the single method→cellID source shared by the chain, bootstrap, and adapter
// (Option 3, #1152).
type CellResolver func(fullMethod string) (cellID string, ok bool)

// UnaryCellAttribution returns the interceptor that attributes each RPC to its
// owning cell: it resolves info.FullMethod through resolve and, on a match,
// writes kernel/ctxkeys.CellID into the context BEFORE the access-log and metrics
// interceptors read it. It is the gRPC analog of the HTTP CellAttribution
// middleware (runtime/http/middleware/cell_id.go) and the writer for the
// otherwise-sentinel grpc_server_* cell label (closing #1383).
//
// resolve is required: a nil resolver is a wiring bug that fails fast at
// construction (programmer-error panic, like UnaryMetrics' nil-collector guard)
// rather than silently leaving every RPC attributed to the runtime sentinel.
func UnaryCellAttribution(resolve CellResolver) grpc.UnaryServerInterceptor {
	if resolve == nil {
		panic(panicregister.Approved("interceptor-cell-attribution-resolver-required",
			errcode.Assertion("interceptor.UnaryCellAttribution: resolver is required")))
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if cellID, ok := resolve(info.FullMethod); ok {
			ctx = kernelctxkeys.WithCellID(ctx, cellID)
		}
		return handler(ctx, req)
	}
}
