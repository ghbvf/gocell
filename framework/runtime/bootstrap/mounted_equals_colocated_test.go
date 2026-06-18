package bootstrap

// mounted_equals_colocated_test.go — red/green + anti-vacuity for the phase0
// MOUNTED-EQUALS-COLOCATED guard (#2278 PR-2).
//
// INVARIANT: MOUNTED-EQUALS-COLOCATED-01
//
// The guard is the upstream backstop for role-based subset mounting: even if a
// composition root bypasses composition.NewForRole and mounts the wrong cell set
// directly (New(allCells).With(allMods)), phase0 fails fast when the mounted cell
// set is not exactly the active topology's colocated set, or when any mounted
// cell is declared remote (the silent double-mount D4 describes). AI-robust
// grade: Medium (runtime bijection; cellID is a runtime string — same honest
// ceiling as M12a / the broker-mandatory gate).

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
)

// mountedAssembly builds an un-started CoreAssembly with the given cell IDs
// registered, modelling the cells THIS process actually mounts.
func mountedAssembly(t *testing.T, ids ...string) *assembly.CoreAssembly {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "mec-test", DurabilityMode: outbox.DurabilityDemo})
	for _, id := range ids {
		if err := asm.Register(cell.MustNewBaseCell(&metadata.CellMeta{ID: id})); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
	}
	return asm
}

// TestValidateMountedEqualsColocated exercises the guard directly (no full Run),
// mirroring TestValidateSplitTopologyBroker.
func TestValidateMountedEqualsColocated(t *testing.T) {
	splitSpec := DeploymentTopologySpec{
		Colocated: []string{"alpha"},
		Remote:    []RemoteCellEndpoint{{CellID: "beta", Endpoint: "edge.svc:9001"}},
	}
	twoColocatedSpec := DeploymentTopologySpec{
		Colocated: []string{"alpha", "gamma"},
		Remote:    []RemoteCellEndpoint{{CellID: "beta", Endpoint: "edge.svc:9001"}},
	}

	cases := []struct {
		name    string
		spec    DeploymentTopologySpec
		mounted []string
		wantErr bool
	}{
		{
			name:    "GREEN: mounted == colocated, no remote mounted",
			spec:    splitSpec,
			mounted: []string{"alpha"},
			wantErr: false,
		},
		{
			name:    "GREEN: zero topology (monolith) — guard does not fire",
			spec:    DeploymentTopologySpec{},
			mounted: []string{"alpha", "beta", "gamma"},
			wantErr: false,
		},
		{
			name:    "RED: a mounted cell is declared remote (silent double-mount)",
			spec:    splitSpec,
			mounted: []string{"alpha", "beta"},
			wantErr: true,
		},
		{
			name:    "RED: a colocated cell is not mounted (missing host)",
			spec:    twoColocatedSpec,
			mounted: []string{"alpha"},
			wantErr: true,
		},
		{
			name:    "RED: a mounted cell is neither colocated nor remote",
			spec:    splitSpec,
			mounted: []string{"alpha", "zeta"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dt, err := newDeploymentTopology(tc.spec)
			if err != nil {
				t.Fatalf("newDeploymentTopology: %v", err)
			}
			b := &Bootstrap{
				deploymentTopology: dt,
				assemblyCore:       mountedAssembly(t, tc.mounted...),
			}
			err = b.validateMountedEqualsColocated()
			if tc.wantErr {
				if err == nil {
					t.Fatal("validateMountedEqualsColocated: expected error, got nil")
				}
				var ec *errcode.Error
				if !errors.As(err, &ec) {
					t.Fatalf("error is not *errcode.Error: %T %v", err, err)
				}
				if ec.Code != errcode.ErrValidationFailed {
					t.Errorf("error code = %s, want %s", ec.Code, errcode.ErrValidationFailed)
				}
			} else if err != nil {
				t.Errorf("validateMountedEqualsColocated: unexpected error: %v", err)
			}
		})
	}
}

// TestPhase0_RejectsMountedRemoteCell verifies the guard is wired into phase0
// end-to-end: a split topology whose mounted assembly includes a remote cell is
// rejected before any side effects start. The broker-mandatory gate is satisfied
// (real-broker kind + non-nil pub/sub) so the ONLY remaining failure is the
// MOUNTED-EQUALS-COLOCATED guard — asserted via its distinctive message.
func TestPhase0_RejectsMountedRemoteCell(t *testing.T) {
	clk := clock.Real()
	splitSpec := DeploymentTopologySpec{
		Colocated: []string{"alpha"},
		Remote:    []RemoteCellEndpoint{{CellID: "beta", Endpoint: "edge.svc:9001"}},
	}
	postgresTopo, err := NewTopology("real", "postgres", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}
	brokerBus := eventbus.New(clk)
	// beta (a remote cell) is wrongly mounted alongside alpha.
	asm := assembly.New(clk, assembly.Config{ID: "mec-phase0", DurabilityMode: outbox.DurabilityDemo})
	for _, id := range []string{"alpha", "beta"} {
		if regErr := asm.Register(cell.MustNewBaseCell(&metadata.CellMeta{ID: id})); regErr != nil {
			t.Fatalf("register %q: %v", id, regErr)
		}
	}

	b := New(
		clk,
		WithDeploymentTopology(splitSpec),
		WithControlPlaneTopology(postgresTopo),
		WithPublisher(brokerBus),
		WithSubscriber(brokerBus),
		WithEventTransportKind(RealBrokerEventTransport()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err = b.phase0ValidateOptions()
	if err == nil {
		t.Fatal("phase0ValidateOptions: expected error when a remote cell is mounted, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error is not *errcode.Error: %T %v", err, err)
	}
	if !strings.Contains(strings.ToLower(ec.Message), "mounted") {
		t.Errorf("error message %q should come from the MOUNTED-EQUALS-COLOCATED guard (mention 'mounted')", ec.Message)
	}
}
