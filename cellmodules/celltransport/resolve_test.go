package celltransport_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cellmodules/celltransport"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

func assertKindInternal(t *testing.T, err error) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInternal {
		t.Errorf("Kind = %v, want KindInternal", ec.Kind)
	}
}

func remoteTopo(t *testing.T, endpoint string) bootstrap.DeploymentTopology {
	t.Helper()
	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Remote: []bootstrap.RemoteCellEndpoint{{CellID: "configcore", Endpoint: endpoint}},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}
	return topo
}

// TestResolve_Colocated verifies that a co-located cellID returns the inProc transport.
func TestResolve_Colocated(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"configcore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	got, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != inProc {
		t.Errorf("Resolve returned %v, want %v (inProc)", got, inProc)
	}
	// Co-located has no remote peer → contributes no readiness probe resource.
	if len(res) != 0 {
		t.Errorf("co-located Resolve returned %d resources, want 0", len(res))
	}
}

// TestResolve_Remote verifies that a remote cellID returns a non-nil
// *transport.RemoteHTTPTransport distinct from inProc.
func TestResolve_Remote(t *testing.T) {
	t.Parallel()

	topo := remoteTopo(t, "127.0.0.1:9090")
	inProc := transport.NewInProcess(nil)
	got, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil {
		t.Fatal("Resolve returned nil for remote cell")
	}
	if got == transport.CellTransport(inProc) {
		t.Error("Resolve returned inProc for a remote cell — expected a RemoteHTTPTransport")
	}
	if _, ok := got.(*transport.RemoteHTTPTransport); !ok {
		t.Errorf("Resolve returned %T for remote cell, want *transport.RemoteHTTPTransport", got)
	}
}

// TestResolve_NilClockPanics verifies that passing a nil clock to Resolve
// triggers a registered panic (clock.MustHaveClock inside Resolve).
func TestResolve_NilClockPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil clock in celltransport.Resolve, got none")
		}
	}()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"configcore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	_, _, _ = celltransport.Resolve(topo, "configcore", inProc, nil, transport.CrossCellObs{})
}

// TestResolve_UnclassifiedCellReturnsKindInternal verifies defense-in-depth for
// cells that are neither co-located nor remote in an explicit topology.
func TestResolve_UnclassifiedCellReturnsKindInternal(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"accesscore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	_, _, resolveErr := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{})
	if resolveErr == nil {
		t.Fatal("expected error for unclassified cell, got nil")
	}
	assertKindInternal(t, resolveErr)
}

// TestResolve_ColocatedNilInProcReturnsError verifies that a nil inProc
// transport for a co-located cell returns KindInternal.
func TestResolve_ColocatedNilInProcReturnsError(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"configcore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	_, _, resolveErr := celltransport.Resolve(topo, "configcore", nil, clock.Real(), transport.CrossCellObs{})
	if resolveErr == nil {
		t.Fatal("expected error for nil inProc, got nil")
	}
	assertKindInternal(t, resolveErr)
}

// TestResolve_ZeroTopoIsColocated verifies that a zero topology (all-colocated
// default) returns the inProc transport for any cellID.
func TestResolve_ZeroTopoIsColocated(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	got, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != inProc {
		t.Errorf("Resolve returned %v, want inProc for zero topo", got)
	}
}

// --- #2251 P1.3: tracer propagation through the sealed bundle ---

// TestResolve_Remote_PropagatesTracer asserts the remote transport opens a span
// with transport_mode=remote using the tracer carried by the CrossCellObs bundle
// (closes ADR D4 span half: remote calls were previously NoopTracer-only).
func TestResolve_Remote_PropagatesTracer(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &recTracer{}
	topo := remoteTopo(t, srv.URL)
	inProc := transport.NewInProcess(nil)

	ct, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.NewCrossCellObs(nil, rec))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://placeholder/internal/v1/config/k", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ct.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	_ = resp.Body.Close()

	if !rec.hasAttr("transport_mode", "remote") {
		t.Error("remote DoContract span missing transport_mode=remote attr (tracer not propagated)")
	}
}

// --- #2251 P2.7: remote-peer readiness probe ---

// TestResolve_Remote_ContributesReadinessProbe asserts the remote branch returns
// exactly one readiness ManagedResource named "<cell>_remote_ready".
func TestResolve_Remote_ContributesReadinessProbe(t *testing.T) {
	t.Parallel()

	topo := remoteTopo(t, "127.0.0.1:9090")
	inProc := transport.NewInProcess(nil)

	_, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("remote Resolve returned %d resources, want 1", len(res))
	}
	probes := res[0].Probes()
	if len(probes) != 1 {
		t.Fatalf("readiness resource exposed %d probes, want 1", len(probes))
	}
	if got := probes[0].Name().String(); got != "configcore_remote_ready" {
		t.Errorf("probe name = %q, want configcore_remote_ready", got)
	}
	// The readiness resource owns no goroutine and nothing to close.
	if res[0].Worker() != nil {
		t.Error("readiness resource Worker() must be nil")
	}
	if err := res[0].Close(context.Background()); err != nil {
		t.Errorf("readiness resource Close() = %v, want nil", err)
	}
}

// TestResolve_RemoteProbe_TCPDial is the cascade-safety core: the probe TCP-dials
// the peer endpoint only (it does NOT issue an HTTP /readyz), so a peer that
// accepts TCP but speaks no HTTP is still reported ready — proving no recursive
// /readyz cascade. A closed endpoint degrades the probe; a canceled ctx errors.
func TestResolve_RemoteProbe_TCPDial(t *testing.T) {
	t.Parallel()

	// A raw TCP listener that accepts connections but never speaks HTTP.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	probe := remoteReadinessProbe(t, ln.Addr().String())

	// Reachable: TCP dial succeeds even though the listener never sends HTTP.
	if err := probe.Check(context.Background()); err != nil {
		t.Errorf("probe.Check on reachable raw-TCP peer = %v, want nil (TCP-only, no /readyz)", err)
	}

	// Canceled ctx must surface as an error (honors the /readyz deadline).
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probe.Check(cctx); err == nil {
		t.Error("probe.Check with canceled ctx = nil, want error")
	}

	// Unreachable: bind+close to get a (very likely) refused address.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(dead): %v", err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	deadProbe := remoteReadinessProbe(t, deadAddr)
	if err := deadProbe.Check(context.Background()); err == nil {
		t.Errorf("probe.Check on closed endpoint %q = nil, want error (peer down → readiness degrades)", deadAddr)
	}
}

// remoteReadinessProbe resolves a remote topology for endpoint and returns the
// single readiness probe its ManagedResource exposes.
func remoteReadinessProbe(t *testing.T, endpoint string) interface {
	Check(context.Context) error
} {
	t.Helper()
	topo := remoteTopo(t, endpoint)
	inProc := transport.NewInProcess(nil)
	_, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res) != 1 || len(res[0].Probes()) != 1 {
		t.Fatalf("expected 1 readiness probe, got resources=%d", len(res))
	}
	return res[0].Probes()[0]
}

// --- local test tracer (celltransport_test cannot reach transport's internal one) ---

type recTracer struct {
	attrs []wrapper.Attr
}

func (rt *recTracer) Start(ctx context.Context, _ string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	rt.attrs = append(rt.attrs, attrs...)
	return ctx, &recSpan{rt: rt}
}

func (rt *recTracer) hasAttr(key string, val any) bool {
	for _, a := range rt.attrs {
		if a.Key == key && a.Value == val {
			return true
		}
	}
	return false
}

type recSpan struct{ rt *recTracer }

func (s *recSpan) SetAttributes(attrs ...wrapper.Attr)  { s.rt.attrs = append(s.rt.attrs, attrs...) }
func (s *recSpan) RecordError(error)                    {}
func (s *recSpan) SetStatus(wrapper.StatusCode, string) {}
func (s *recSpan) End()                                 {}
