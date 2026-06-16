package transport

import (
	"net"

	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// CrossCellObs is the sealed cross-cell observability bundle: the metrics +
// tracer a remote [CellTransport] needs, carried as ONE value (#2251 P1.3).
//
// It exists to make the asymmetric "wired metrics but forgot the tracer" state
// unrepresentable at the celltransport.Resolve boundary: both observability
// deps travel together, minted ONCE by composition.Builder from a single source
// (SharedDeps.Tracer + the shared transport metrics), so the remote transport
// gets the SAME tracer bootstrap wires for in-process calls instead of the
// previously hard-coded nil. The fields are unexported; the only minting path is
// [NewCrossCellObs]. A zero-value bundle is a valid "no observability" input
// (nil metrics → no recording; nil tracer → consumers degrade to NoopTracer).
type CrossCellObs struct {
	metrics *Metrics
	tracer  wrapper.Tracer
}

// NewCrossCellObs mints a bundle. A nil tracer is normalized to
// wrapper.NoopTracer{} (same contract as [NewRemoteHTTP]) so a minted bundle
// never carries a nil tracer. A nil metrics is preserved (records nothing).
func NewCrossCellObs(metrics *Metrics, tracer wrapper.Tracer) CrossCellObs {
	if tracer == nil {
		tracer = wrapper.NoopTracer{}
	}
	return CrossCellObs{metrics: metrics, tracer: tracer}
}

// Metrics returns the shared transport metrics (may be nil → no recording).
func (o CrossCellObs) Metrics() *Metrics { return o.metrics }

// Tracer returns the bundle's tracer. It is non-nil for any bundle produced by
// [NewCrossCellObs]; a zero-value bundle returns nil (consumers degrade nil to
// NoopTracer).
func (o CrossCellObs) Tracer() wrapper.Tracer { return o.tracer }

// msgEndpointDialInvalid — MESSAGE-CONST-LITERAL-01.
const msgEndpointDialInvalid = "transport: endpoint is not a valid host:port dial target"

// EndpointDialTarget resolves a topology endpoint to a "host:port" suitable for
// a readiness TCP dial (#2251 P2.7). It reuses [parseEndpoint] (the single
// endpoint parser shared with rewriteToAbsolute) and supplies the scheme's
// default port when the authority omits one (http→80, https→443). A bare
// "host:port" is returned unchanged; an unparseable endpoint returns KindInternal
// (a static wiring error, not a transient network condition).
func EndpointDialTarget(endpoint string) (string, error) {
	scheme, host, ok := parseEndpoint(endpoint)
	if !ok {
		return "", errcode.New(errcode.KindInternal, errcode.ErrInternal, msgEndpointDialInvalid,
			errcode.WithInternal(errcode.InternalAttr("endpoint", endpoint)))
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, nil // already host:port
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(host, port), nil
}
