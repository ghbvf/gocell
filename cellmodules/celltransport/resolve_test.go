package celltransport_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cellmodules/celltransport"
	"github.com/ghbvf/gocell/framework/kernel/clock"
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
	got, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != inProc {
		t.Errorf("Resolve returned %v, want %v (inProc)", got, inProc)
	}
}

// TestResolve_Remote verifies that a remote cellID returns a non-nil CellTransport
// (RemoteHTTPTransport) distinct from inProc.
func TestResolve_Remote(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Remote: []bootstrap.RemoteCellEndpoint{
			{CellID: "configcore", Endpoint: "127.0.0.1:9090"},
		},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	got, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil {
		t.Fatal("Resolve returned nil for remote cell")
	}
	if got == transport.CellTransport(inProc) {
		t.Error("Resolve returned inProc for a remote cell — expected a RemoteHTTPTransport")
	}
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
	_, resolveErr := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), nil, nil)
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

	_, resolveErr := celltransport.Resolve(topo, "configcore", nil, clock.Real(), nil, nil)
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
	got, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != inProc {
		t.Errorf("Resolve returned %v, want inProc for zero topo", got)
	}
}
