package celltransport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/worker"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/netutil"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// remoteHTTPClientTimeout is the backstop timeout for the http.Client used in
// remote transport. It is intentionally much larger than any typical caller
// context budget (e.g. configclient 5s) so the caller's ctx cancellation fires
// first in all normal paths. This value exists solely to prevent goroutine leaks
// from unbounded-context callers — it is NOT a second per-request budget.
const remoteHTTPClientTimeout = 30 * time.Second

// remoteReadinessDialTimeout is the backstop dial timeout for the readiness
// probe. DialContext uses the earlier of this and the ctx deadline, so the
// /readyz aggregator deadline (bootstrap.WithReadyzDeadline) still binds when
// tighter; this backstop only guards a caller that passes a deadline-less ctx,
// preventing a SYN-blackhole peer from hanging the probe until the kernel TCP
// timeout (#2251 review F2).
const remoteReadinessDialTimeout = 3 * time.Second

// Error messages — MESSAGE-CONST-LITERAL-01.
const (
	msgNilInProc = "celltransport.Resolve: InProcessTransport must be set" +
		" for a co-located cell (composition.Builder.Build mints it)"
	// msgUnclassifiedCell is the error message for a cellID that is neither
	// co-located nor remote in an explicit topology (TOPO-11 normally prevents
	// this at static-analysis time; this is defense-in-depth).
	msgUnclassifiedCell = "celltransport.Resolve: cellID not classified in deployment topology" +
		" (neither co-located nor remote); gocell validate TOPO-11 prevents this at build time"
	// msgPlaintextNonLoopback rejects a non-loopback peer reached over plaintext
	// (#2263): mTLS is mandatory across a real network boundary. The gocell
	// validate TOPO gate also rejects this at build time; this is the runtime
	// defense-in-depth (and removes the old private-network plaintext fallback).
	msgPlaintextNonLoopback = "celltransport.Resolve: non-loopback remote peer must use an https endpoint (mTLS);" +
		" plaintext across a network boundary is forbidden (gocell validate rejects this at build time)"
	// msgMissingClientTLS rejects an https peer when no client mTLS identity was
	// provisioned — fail-closed rather than silently dial without a client cert.
	msgMissingClientTLS = "celltransport.Resolve: https remote peer requires a client mTLS identity, but none was provisioned;" +
		" set GOCELL_TRANSPORT_TLS_* + GOCELL_SPIFFE_TRUST_DOMAIN (see cellmodules/celltls)"
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
//   - clientTLS: the cell's client mTLS identity (cellmodules/celltls.Resolve).
//     The zero value means "no client mTLS" (demo / loopback). For an https peer
//     endpoint a per-target *tls.Config is minted (peer authenticated by SPIFFE
//     cell ID); see [remoteClientTLSConfig] for the fail-closed gate (#2263).
//
// Returns the selected transport plus the readiness ManagedResources it
// contributes:
//
//   - co-located → (inProc, nil, nil): the in-process peer shares this process,
//     so there is no remote endpoint to health-check.
//   - remote → (RemoteHTTPTransport, [remote-readiness probe], nil): a readiness
//     probe for the declared endpoint so an unreachable peer degrades this cell's
//     /readyz (lets ops shed traffic) without killing liveness (#2251 P2.7). For
//     an mTLS (https) peer the probe completes a TLS handshake (validates chain +
//     peer SPIFFE-ID), otherwise a plain TCP dial; both are cascade-safe (no peer
//     /readyz call).
//   - un-classified → (nil, nil, KindInternal): defense-in-depth (TOPO-11
//     normally prevents this at static-analysis time).
//   - non-loopback plaintext / https-without-identity → (nil, nil, KindInternal):
//     the #2263 fail-closed mTLS gate.
func Resolve(
	topo bootstrap.DeploymentTopology,
	cellID string,
	inProc *transport.InProcessTransport,
	clk clock.Clock,
	obs transport.CrossCellObs,
	clientTLS tlsutil.ClientIdentity,
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

	tlsCfg, err := remoteClientTLSConfig(cellID, endpoint, clientTLS)
	if err != nil {
		return nil, nil, err
	}

	readiness, err := remoteReadiness(cellID, endpoint, tlsCfg)
	if err != nil {
		return nil, nil, err
	}

	httpClient := &http.Client{Timeout: remoteHTTPClientTimeout}
	if tlsCfg != nil {
		httpClient.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	resolver := transport.NewStaticResolver(map[string]string{cellID: endpoint})
	ct := transport.NewRemoteHTTP(clk, cellID, resolver, httpClient, obs.Metrics(), obs.Tracer())
	return ct, []lifecycle.ManagedResource{readiness}, nil
}

// remoteClientTLSConfig decides the client TLS config for a remote peer from the
// endpoint scheme — the #2263 fail-closed mTLS gate:
//
//   - https endpoint → mTLS required: returns a per-peer *tls.Config that
//     authenticates the server by its SPIFFE cell ID. Fails closed if clientTLS
//     is the zero identity (no material provisioned).
//   - non-loopback, non-https endpoint → fails closed: plaintext across a network
//     boundary is forbidden (the gocell validate TOPO gate also rejects this at
//     build time; this is the runtime defense-in-depth).
//   - loopback, non-https endpoint → nil: plaintext is allowed for local
//     multi-process dev / demo.
func remoteClientTLSConfig(cellID, endpoint string, clientTLS tlsutil.ClientIdentity) (*tls.Config, error) {
	if !strings.HasPrefix(endpoint, "https://") {
		if !netutil.IsLoopbackEndpoint(endpoint) {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgPlaintextNonLoopback,
				errcode.WithInternal(errcode.InternalAttr("cellID", cellID), errcode.InternalAttr("endpoint", endpoint)))
		}
		// nil config + nil error is intentional: a loopback peer (local dev) is
		// plaintext-eligible, so there is no client TLS config to build — distinct
		// from an error. Callers branch on the returned *tls.Config being nil.
		return nil, nil //nolint:nilnil // nil cfg + nil err = "loopback plaintext, no client TLS"; see comment above
	}
	if clientTLS.IsZero() {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgMissingClientTLS,
			errcode.WithInternal(errcode.InternalAttr("cellID", cellID)))
	}
	cfg, err := clientTLS.ConfigForPeer(cellID)
	if err != nil {
		return nil, fmt.Errorf("celltransport: build client mTLS config for cell %q: %w", cellID, err)
	}
	return cfg, nil
}

// remoteReadiness builds the readiness ManagedResource for a remote peer
// endpoint. The dial target and typed probe name are resolved eagerly so an
// invalid endpoint / cellID fails fast at Resolve time rather than per /readyz
// invocation.
//
// The probe never issues an HTTP /readyz to the peer — so it reports reachability
// without recursively importing the peer's own readiness (cascade-safe: a peer
// that depends on this cell cannot deadlock both /readyz endpoints). A failed
// probe degrades readiness (this cell returns /readyz 503) but never kills
// liveness (the probe joins the readiness aggregator, not a liveness gate).
//
// When tlsCfg is non-nil (an mTLS https peer) the probe completes a TLS handshake
// rather than a bare TCP dial: this validates the full mTLS path (server chain +
// peer SPIFFE-ID via tlsCfg.VerifyConnection AND that the server accepts this
// cell's client cert) so a cert/trust misconfiguration degrades readiness instead
// of surfacing only on the first real request. The handshake is still
// cascade-safe (it completes at TLS layer, before any HTTP). When tlsCfg is nil
// (plaintext loopback/demo peer) a plain TCP dial proves reachability (#2251 F1).
//
// Total probe time is bounded by the caller ctx deadline (typically the /readyz
// aggregator deadline set by bootstrap.WithReadyzDeadline). The 3 s dial backstop
// (remoteReadinessDialTimeout) only guards a caller that passes a deadline-less
// ctx, preventing a SYN-blackhole peer from stalling the probe until the kernel
// TCP timeout. It does NOT add a second, independent budget — the handshake
// (when mTLS) runs inside the same ctx that constrained the TCP dial.
func remoteReadiness(cellID, endpoint string, tlsCfg *tls.Config) (lifecycle.ManagedResource, error) {
	target, err := transport.EndpointDialTarget(endpoint)
	if err != nil {
		return nil, fmt.Errorf("celltransport: remote readiness dial target for cell %q: %w", cellID, err)
	}
	name, err := healthz.RemoteCellReadyProbeName(cellID)
	if err != nil {
		return nil, fmt.Errorf("celltransport: remote readiness probe name for cell %q: %w", cellID, err)
	}
	probe := healthz.NewProbe(name, func(ctx context.Context) error {
		return dialPeerReadiness(ctx, target, tlsCfg)
	})
	return remoteReadinessResource{probe: probe}, nil
}

// dialPeerReadiness dials target and (for an mTLS peer) completes a TLS
// handshake within the SAME ctx budget. A Close error is a local cleanup concern
// unrelated to peer health, so it must NOT degrade readiness (#2251 review F1).
//
// The total probe time is bounded by the caller ctx deadline (the /readyz
// aggregator deadline) OR the 3 s dial backstop (remoteReadinessDialTimeout) when
// the ctx has no deadline — whichever fires first. The handshake does NOT add a
// fresh remoteReadinessDialTimeout on top of the TCP dial: doing so (F6 fix)
// would create a ~6 s worst case that can exceed the ~5 s /readyz aggregator
// budget and surface a ctx-timeout instead of the real handshake failure.
func dialPeerReadiness(ctx context.Context, target string, tlsCfg *tls.Config) error {
	conn, err := (&net.Dialer{Timeout: remoteReadinessDialTimeout}).DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	if tlsCfg == nil {
		_ = conn.Close() // plaintext: TCP reachability is the readiness signal.
		return nil
	}
	tlsConn := tls.Client(conn, tlsCfg)
	defer func() { _ = tlsConn.Close() }()
	return tlsConn.HandshakeContext(ctx)
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

func (r remoteReadinessResource) Probes() []healthz.Probe   { return []healthz.Probe{r.probe} }
func (remoteReadinessResource) Worker() worker.Worker       { return nil }
func (remoteReadinessResource) Close(context.Context) error { return nil }
