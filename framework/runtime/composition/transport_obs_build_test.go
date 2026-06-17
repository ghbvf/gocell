package composition

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// distinctTracer is a non-Noop wrapper.Tracer used to prove Build threads the
// SAME tracer instance from SharedDeps.Tracer into the minted bundle.
type distinctTracer struct{}

func (distinctTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	return wrapper.NoopTracer{}.Start(ctx, name, attrs...)
}

func nopRuntimeOpts([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil }

// TestBuild_PopulatesTransportObs_ThreadsTracer asserts Build mints the
// cross-cell observability bundle into SharedDeps.TransportObs AND threads
// SharedDeps.Tracer into it — the single-source guarantee (#2251 P1.3): the
// remote transport gets the SAME tracer bootstrap wires, not a forgotten nil.
func TestBuild_PopulatesTransportObs_ThreadsTracer(t *testing.T) {
	ctx := context.Background()

	shared := minimalSharedDeps(t)
	tr := distinctTracer{}
	shared.Tracer = tr

	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	_, err := New("mod1").With(m1).Build(ctx, shared, nopRuntimeOpts)
	require.NoError(t, err)

	if shared.TransportObs.Metrics() == nil {
		t.Error("Build did not mint transport metrics into TransportObs")
	}
	if shared.TransportObs.Tracer() != tr {
		t.Errorf("Build did not thread SharedDeps.Tracer into TransportObs: got %T", shared.TransportObs.Tracer())
	}
}

// TestBuild_TransportObs_NilTracerDegradesToNoop asserts the default (no tracer
// configured) path: the bundle carries a NoopTracer, not a nil — so a remote
// DoContract never panics on a nil tracer. Seam-only: production stays NoopTracer
// until a composition root sets SharedDeps.Tracer.
func TestBuild_TransportObs_NilTracerDegradesToNoop(t *testing.T) {
	ctx := context.Background()

	shared := minimalSharedDeps(t) // Tracer unset (nil)

	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	_, err := New("mod1").With(m1).Build(ctx, shared, nopRuntimeOpts)
	require.NoError(t, err)

	if _, ok := shared.TransportObs.Tracer().(wrapper.NoopTracer); !ok {
		t.Errorf("nil SharedDeps.Tracer must degrade bundle to NoopTracer, got %T", shared.TransportObs.Tracer())
	}
}
