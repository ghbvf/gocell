package transport

import (
	"net"

	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
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

// NewCrossCellObs mints a bundle. A nil OR typed-nil tracer is normalized to
// wrapper.NoopTracer{} (same contract as [NewRemoteHTTP]) so a minted bundle
// never carries a tracer that would panic on Start. The typed-nil case (a
// non-nil wrapper.Tracer interface wrapping a nil pointer) is caught by
// validation.IsNilInterface — a bare `tracer == nil` would miss it and ship the
// typed-nil into the remote transport (#2251 review F2). A nil metrics is
// preserved (records nothing).
func NewCrossCellObs(metrics *Metrics, tracer wrapper.Tracer) CrossCellObs {
	if validation.IsNilInterface(tracer) {
		tracer = wrapper.NoopTracer{}
	}
	return CrossCellObs{metrics: metrics, tracer: tracer}
}

// Metrics returns the shared transport metrics (may be nil → no recording).
func (o CrossCellObs) Metrics() *Metrics { return o.metrics }

// Tracer returns the bundle's tracer. Two cases, by construction path:
//   - a bundle minted by [NewCrossCellObs] always returns a non-nil tracer (nil
//     input was normalized to wrapper.NoopTracer{} at mint time) — this is the
//     production path (composition.Builder always mints via NewCrossCellObs).
//   - a zero-value CrossCellObs{} (test-only "no observability" input) returns
//     nil; the sole consumer [NewRemoteHTTP] degrades a nil tracer to NoopTracer,
//     so a zero-value bundle is functionally equivalent to NewCrossCellObs(nil, nil).
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
