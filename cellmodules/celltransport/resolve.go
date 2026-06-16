package celltransport

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// remoteHTTPClientTimeout is the backstop timeout for the http.Client used in
// remote transport. It is intentionally much larger than any typical caller
// context budget (e.g. configclient 5s) so the caller's ctx cancellation fires
// first in all normal paths. This value exists solely to prevent goroutine leaks
// from unbounded-context callers — it is NOT a second per-request budget.
const remoteHTTPClientTimeout = 30 * time.Second

// Error messages — MESSAGE-CONST-LITERAL-01.
const (
	msgNilInProc = "celltransport.Resolve: InProcessTransport must be set" +
		" for a co-located cell (composition.Builder.Build mints it)"
	// msgUnclassifiedCell is the error message for a cellID that is neither
	// co-located nor remote in an explicit topology (TOPO-11 normally prevents
	// this at static-analysis time; this is defense-in-depth).
	msgUnclassifiedCell = "celltransport.Resolve: cellID not classified in deployment topology" +
		" (neither co-located nor remote); gocell validate TOPO-11 prevents this at build time"
)

// Resolve selects the [transport.CellTransport] for cellID based on the sealed
// deployment topology. It is the SOLE sanctioned construction site for a
// RemoteHTTPTransport in wiring-layer packages (CELLTRANSPORT-SELECT-FUNNEL-01).
//
// Parameters:
//   - topo: the sealed deployment topology (obtained via
//     bootstrap.NewDeploymentTopology from the assembly spec).
//   - cellID: the target cell whose transport to resolve.
//   - inProc: the shared in-process transport; must be non-nil (the composition
//     root's Builder.Build always populates it).
//   - clk: mandatory positional clock (clock.MustHaveClock, ADR clock-positional).
//   - metrics: transport metrics for the remote transport; nil = no recording.
//   - tracer: tracing backend for the remote transport; nil = no-op.
//
// Selection logic:
//
//   - co-located → returns inProc.
//   - remote → builds and returns a *transport.RemoteHTTPTransport targeting
//     the declared endpoint, using a transport.StaticResolver over the topology's
//     remote endpoint map.
//   - un-classified → returns (nil, KindInternal) as defense-in-depth (TOPO-11
//     normally prevents this at static-analysis time).
func Resolve(
	topo bootstrap.DeploymentTopology,
	cellID string,
	inProc *transport.InProcessTransport,
	clk clock.Clock,
	metrics *transport.Metrics,
	tracer wrapper.Tracer,
) (transport.CellTransport, error) {
	clock.MustHaveClock(clk, "celltransport.Resolve")

	if topo.IsColocated(cellID) {
		if inProc == nil {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				msgNilInProc,
				errcode.WithInternal(errcode.InternalAttr("cellID", cellID)))
		}
		return inProc, nil
	}

	endpoint, ok := topo.RemoteEndpoint(cellID)
	if !ok {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			msgUnclassifiedCell,
			errcode.WithInternal(errcode.InternalAttr("cellID", cellID)))
	}

	resolver := transport.NewStaticResolver(map[string]string{cellID: endpoint})
	return transport.NewRemoteHTTP(clk, cellID, resolver, &http.Client{Timeout: remoteHTTPClientTimeout}, metrics, tracer), nil
}
