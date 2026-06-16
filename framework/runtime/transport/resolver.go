package transport

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Resolver maps a target cellID to its remote network endpoint at dispatch
// time. It is the seam between transport selection (topology-gated, at wiring
// time) and per-call address resolution (Resolver.Resolve, at request time).
//
// The only current implementation is [StaticResolver], which consults a static
// map of cellID→endpoint built once at startup from the sealed deployment
// topology. Future implementations (DNS-based, service-discovery-backed)
// satisfy this interface without changing [RemoteHTTPTransport].
type Resolver interface {
	Resolve(ctx context.Context, cellID string) (endpoint string, err error)
}

// msgResolverCellNotRemote is the const-literal error message emitted when
// the requested cellID has no remote endpoint in the deployment topology.
// This is a wiring error (KindInternal), not a transient network failure.
const msgResolverCellNotRemote = "static resolver: cellID has no remote endpoint in the deployment topology (wiring error)"

// StaticResolver is a [Resolver] backed by a static cellID→endpoint map built
// once at startup from the sealed deployment topology. It returns a KindInternal
// error for any cellID not present in the map — either colocated or absent from
// the topology — both indicating a wiring bug, not a transient network failure.
//
// StaticResolver is intentionally non-caching and lock-free: the endpoint map
// is immutable (WriteOnce) so concurrent Resolve calls require no
// synchronization.
//
// Construction: use [NewStaticResolver] (from an explicit endpoint map).
// Callers in cellmodules/ that have a [bootstrap.DeploymentTopology] use
// [cellmodules/celltransport.Resolve], which extracts the remote endpoints
// and calls [NewStaticResolver] — keeping the bootstrap→transport import
// direction intact.
type StaticResolver struct {
	// endpoints is the immutable cellID→endpoint map. An endpoint is a bare
	// host:port or an http(s):// URL (as stored in DeploymentTopology).
	endpoints map[string]string
}

// NewStaticResolver constructs a StaticResolver over the given cellID→endpoint
// map. The map is copied at construction time so callers may safely reuse or
// discard the original. A nil map yields a resolver that always returns
// KindInternal (no remote cells declared).
func NewStaticResolver(endpoints map[string]string) *StaticResolver {
	m := make(map[string]string, len(endpoints))
	for k, v := range endpoints {
		m[k] = v
	}
	return &StaticResolver{endpoints: m}
}

// Resolve returns the declared remote endpoint for cellID. If cellID is not
// present in the static endpoint map, Resolve returns a KindInternal error —
// the cellID→transport mapping is established at wiring time, and a missing
// mapping is a programmer/wiring error, not a transient network failure.
func (r *StaticResolver) Resolve(_ context.Context, cellID string) (string, error) {
	ep, ok := r.endpoints[cellID]
	if !ok {
		return "", errcode.New(errcode.KindInternal, errcode.ErrInternal,
			msgResolverCellNotRemote,
			errcode.WithInternal(errcode.InternalAttr("cellID", cellID)))
	}
	return ep, nil
}

// compile-time: StaticResolver satisfies the Resolver interface.
var _ Resolver = (*StaticResolver)(nil)
