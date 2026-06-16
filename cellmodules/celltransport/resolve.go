package celltransport

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/worker"
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
//   - inProc: the shared in-process transport; must be non-nil for a co-located
//     cell (the composition root's Builder.Build always populates it).
//   - clk: mandatory positional clock (clock.MustHaveClock, ADR clock-positional).
//   - obs: the SINGLE-SOURCE cross-cell observability bundle (metrics + tracer)
//     minted by composition.Builder. Passing one bundle (rather than separate
//     metrics + tracer args) makes "wired metrics but forgot the tracer"
//     unrepresentable at this boundary (#2251 P1.3). A zero-value bundle = no
//     observability (nil metrics, NoopTracer).
//
// Returns the selected transport plus the readiness ManagedResources it
// contributes:
//
//   - co-located → (inProc, nil, nil): the in-process peer shares this process,
//     so there is no remote endpoint to health-check.
//   - remote → (RemoteHTTPTransport, [remote-readiness probe], nil): a TCP-dial
//     readiness probe for the declared endpoint so an unreachable peer degrades
//     this cell's /readyz (lets ops shed traffic) without killing liveness
//     (#2251 P2.7).
//   - un-classified → (nil, nil, KindInternal): defense-in-depth (TOPO-11
//     normally prevents this at static-analysis time).
func Resolve(
	topo bootstrap.DeploymentTopology,
	cellID string,
	inProc *transport.InProcessTransport,
	clk clock.Clock,
	obs transport.CrossCellObs,
) (transport.CellTransport, []lifecycle.ManagedResource, error) {
	clock.MustHaveClock(clk, "celltransport.Resolve")

	if topo.IsColocated(cellID) {
		if inProc == nil {
			return nil, nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				msgNilInProc,
				errcode.WithInternal(errcode.InternalAttr("cellID", cellID)))
		}
		return inProc, nil, nil
	}

	endpoint, ok := topo.RemoteEndpoint(cellID)
	if !ok {
		return nil, nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			msgUnclassifiedCell,
			errcode.WithInternal(errcode.InternalAttr("cellID", cellID)))
	}

	readiness, err := remoteReadiness(cellID, endpoint)
	if err != nil {
		return nil, nil, err
	}

	resolver := transport.NewStaticResolver(map[string]string{cellID: endpoint})
	ct := transport.NewRemoteHTTP(clk, cellID, resolver,
		&http.Client{Timeout: remoteHTTPClientTimeout}, obs.Metrics(), obs.Tracer())
	return ct, []lifecycle.ManagedResource{readiness}, nil
}

// remoteReadiness builds the TCP-dial readiness ManagedResource for a remote
// peer endpoint. The dial target and typed probe name are resolved eagerly so an
// invalid endpoint / cellID fails fast at Resolve time rather than per /readyz
// invocation.
//
// The probe TCP-dials the peer's resolved host:port ONLY — it never issues an
// HTTP /readyz — so it reports reachability without recursively importing the
// peer's own readiness (cascade-safe: a peer that depends on this cell cannot
// deadlock both /readyz endpoints). A failed dial degrades readiness (this cell
// returns /readyz 503) but never kills liveness (the probe joins the readiness
// aggregator, not a liveness gate).
func remoteReadiness(cellID, endpoint string) (lifecycle.ManagedResource, error) {
	target, err := transport.EndpointDialTarget(endpoint)
	if err != nil {
		return nil, err
	}
	name, err := healthz.RemoteCellReadyProbeName(cellID)
	if err != nil {
		return nil, err
	}
	probe := healthz.NewProbe(name, func(ctx context.Context) error {
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", target)
		if dialErr != nil {
			return dialErr
		}
		return conn.Close()
	})
	return remoteReadinessResource{probe: probe}, nil
}

// remoteReadinessResource adapts a single readiness probe to the ManagedResource
// contract so a cell module can contribute it via ModuleResult.Resources
// (WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01). It owns no background goroutine and
// holds nothing to close: the endpoint is a startup-period snapshot of the sealed
// (static) deployment topology, and the per-Check dial connection is closed in
// the probe itself.
type remoteReadinessResource struct {
	probe healthz.Probe
}

func (r remoteReadinessResource) Probes() []healthz.Probe { return []healthz.Probe{r.probe} }
func (remoteReadinessResource) Worker() worker.Worker     { return nil }
func (remoteReadinessResource) Close(context.Context) error { return nil }
