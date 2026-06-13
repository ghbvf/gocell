package bootstrap

// deployment_topology_test.go — tests for DeploymentTopology WriteOnce API.
//
// INVARIANT: DEPLOYMENT-TOPOLOGY-SEALED-FIELD-FROZEN-01

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// DEPLOYMENT-TOPOLOGY-SEALED-FIELD-FROZEN-01
// ---------------------------------------------------------------------------

// TestDeploymentTopologyZeroExportedFields enforces the sealed-construction
// invariant: a DeploymentTopology must have zero exported fields so the only
// way to populate one is via newDeploymentTopology (the sole validated
// constructor). If any field is exported, package-external code could build an
// unvalidated DeploymentTopology via a struct literal, defeating the seal.
//
// INVARIANT: DEPLOYMENT-TOPOLOGY-SEALED-FIELD-FROZEN-01
//
// Package-internal blind spot (in-package literal bypasses newDeploymentTopology)
// is the Go package visibility ceiling. This reflect freeze is the Medium
// backstop; Hard-ening path: generate DeploymentTopology from assembly.yaml
// codegen (Batch 3+).
func TestDeploymentTopologyZeroExportedFields(t *testing.T) {
	rt := reflect.TypeOf(DeploymentTopology{})
	for i := 0; i < rt.NumField(); i++ {
		if f := rt.Field(i); f.IsExported() {
			t.Errorf("DeploymentTopology.%s is exported; all fields must be unexported so the only "+
				"construction path is newDeploymentTopology (validated)", f.Name)
		}
	}
}

// TestDeploymentTopologyExpectedUnexportedFields ensures the exact unexported
// field set is frozen (anti-drift companion to the exported-zero test above).
func TestDeploymentTopologyExpectedUnexportedFields(t *testing.T) {
	wantFields := map[string]bool{
		"explicit":  false,
		"colocated": false,
		"remote":    false,
	}
	rt := reflect.TypeOf(DeploymentTopology{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if _, ok := wantFields[name]; !ok {
			t.Errorf("unexpected field %q in DeploymentTopology; update DEPLOYMENT-TOPOLOGY-SEALED-FIELD-FROZEN-01 test", name)
		}
		wantFields[name] = true
	}
	for name, seen := range wantFields {
		if !seen {
			t.Errorf("expected field %q not found in DeploymentTopology", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Zero value (all-colocated)
// ---------------------------------------------------------------------------

func TestDeploymentTopologyZeroValue_IsColocatedAlwaysTrue(t *testing.T) {
	var dt DeploymentTopology
	cases := []string{"cellA", "cellB", "", "unknown"}
	for _, id := range cases {
		if !dt.IsColocated(id) {
			t.Errorf("zero-value DeploymentTopology.IsColocated(%q) = false, want true (all-colocated)", id)
		}
	}
}

func TestDeploymentTopologyZeroValue_RemoteEndpointAlwaysMiss(t *testing.T) {
	var dt DeploymentTopology
	cases := []string{"cellA", "cellB", "", "unknown"}
	for _, id := range cases {
		ep, ok := dt.RemoteEndpoint(id)
		if ok {
			t.Errorf("zero-value DeploymentTopology.RemoteEndpoint(%q) = (%q, true), want (\"\", false)", id, ep)
		}
	}
}

// ---------------------------------------------------------------------------
// newDeploymentTopology: table-driven happy paths
// ---------------------------------------------------------------------------

func TestNewDeploymentTopology_HappyPath(t *testing.T) {
	cases := []struct {
		name            string
		spec            DeploymentTopologySpec
		isColocated     map[string]bool
		remoteEndpoints map[string]string // cellID -> expected endpoint
	}{
		{
			name: "empty spec -> all-colocated (zero topology)",
			spec: DeploymentTopologySpec{},
			isColocated: map[string]bool{
				"cellA": true,
				"cellB": true,
			},
			remoteEndpoints: map[string]string{},
		},
		{
			name: "explicit colocated cells",
			spec: DeploymentTopologySpec{
				Colocated: []string{"cellA", "cellB"},
			},
			isColocated: map[string]bool{
				"cellA":   true,
				"cellB":   true,
				"cellC":   false,
				"unknown": false,
			},
			remoteEndpoints: map[string]string{},
		},
		{
			name: "explicit remote cell",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{
					{CellID: "cellRemote", Endpoint: "cell-remote:8080"},
				},
			},
			isColocated: map[string]bool{
				"cellRemote": false,
				"cellOther":  false,
			},
			remoteEndpoints: map[string]string{
				"cellRemote": "cell-remote:8080",
			},
		},
		{
			name: "mixed colocated and remote",
			spec: DeploymentTopologySpec{
				Colocated: []string{"cellA", "cellB"},
				Remote: []RemoteCellEndpoint{
					{CellID: "cellC", Endpoint: "cell-c:9090"},
					{CellID: "cellD", Endpoint: "http://cell-d.internal"},
				},
			},
			isColocated: map[string]bool{
				"cellA": true,
				"cellB": true,
				"cellC": false,
				"cellD": false,
				"cellE": false,
			},
			remoteEndpoints: map[string]string{
				"cellC": "cell-c:9090",
				"cellD": "http://cell-d.internal",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dt, err := newDeploymentTopology(tc.spec)
			if err != nil {
				t.Fatalf("newDeploymentTopology: unexpected error: %v", err)
			}

			for cellID, wantColocated := range tc.isColocated {
				if got := dt.IsColocated(cellID); got != wantColocated {
					t.Errorf("IsColocated(%q) = %v, want %v", cellID, got, wantColocated)
				}
			}

			for cellID, wantEndpoint := range tc.remoteEndpoints {
				ep, ok := dt.RemoteEndpoint(cellID)
				if !ok {
					t.Errorf("RemoteEndpoint(%q): got miss, want hit %q", cellID, wantEndpoint)
				} else if ep != wantEndpoint {
					t.Errorf("RemoteEndpoint(%q) = %q, want %q", cellID, ep, wantEndpoint)
				}
			}

			// Cells not in remoteEndpoints should miss
			for cellID := range tc.isColocated {
				if _, hasExpected := tc.remoteEndpoints[cellID]; !hasExpected {
					_, ok := dt.RemoteEndpoint(cellID)
					// Only for empty-spec (all-colocated), every RemoteEndpoint is a miss.
					// For explicit specs, non-remote cells should also miss.
					if ok {
						t.Errorf("RemoteEndpoint(%q): expected miss for non-remote cell", cellID)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// newDeploymentTopology: validation reds
// ---------------------------------------------------------------------------

func TestNewDeploymentTopology_ValidationErrors(t *testing.T) {
	cases := []struct {
		name        string
		spec        DeploymentTopologySpec
		wantErrCode errcode.Code
		wantMsgPart string
	}{
		{
			name: "mutual exclusion: cell in both colocated and remote",
			spec: DeploymentTopologySpec{
				Colocated: []string{"cellA"},
				Remote:    []RemoteCellEndpoint{{CellID: "cellA", Endpoint: "cell-a:8080"}},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "both colocated and remote",
		},
		{
			name: "duplicate cellID in colocated",
			spec: DeploymentTopologySpec{
				Colocated: []string{"cellA", "cellA"},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "duplicate",
		},
		{
			name: "duplicate cellID in remote",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{
					{CellID: "cellA", Endpoint: "cell-a:8080"},
					{CellID: "cellA", Endpoint: "cell-a-2:8080"},
				},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "duplicate",
		},
		{
			name: "empty cellID in colocated",
			spec: DeploymentTopologySpec{
				Colocated: []string{""},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "cellID",
		},
		{
			name: "whitespace-only cellID in colocated",
			spec: DeploymentTopologySpec{
				Colocated: []string{"   "},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "cellID",
		},
		{
			name: "empty cellID in remote",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{{CellID: "", Endpoint: "cell:8080"}},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "cellID",
		},
		{
			name: "empty endpoint in remote",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{{CellID: "cellA", Endpoint: ""}},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "endpoint",
		},
		{
			name: "malformed endpoint (no host, no port)",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{{CellID: "cellA", Endpoint: "://bad"}},
			},
			wantErrCode: errcode.ErrValidationFailed,
			wantMsgPart: "endpoint",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newDeploymentTopology(tc.spec)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("error is not *errcode.Error: %T %v", err, err)
			}
			if ec.Code != tc.wantErrCode {
				t.Errorf("error code = %s, want %s", ec.Code, tc.wantErrCode)
			}
			if !strings.Contains(strings.ToLower(ec.Message), strings.ToLower(tc.wantMsgPart)) {
				t.Errorf("error message %q does not contain %q", ec.Message, tc.wantMsgPart)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsColocated / RemoteEndpoint: explicit topology
// ---------------------------------------------------------------------------

func TestDeploymentTopology_IsColocated_ExplicitTopology(t *testing.T) {
	dt, err := newDeploymentTopology(DeploymentTopologySpec{
		Colocated: []string{"cellA", "cellB"},
		Remote:    []RemoteCellEndpoint{{CellID: "cellC", Endpoint: "cell-c:8080"}},
	})
	if err != nil {
		t.Fatalf("newDeploymentTopology: %v", err)
	}

	if !dt.IsColocated("cellA") {
		t.Error("IsColocated(cellA) = false, want true")
	}
	if !dt.IsColocated("cellB") {
		t.Error("IsColocated(cellB) = false, want true")
	}
	if dt.IsColocated("cellC") {
		t.Error("IsColocated(cellC) = true, want false (it is remote)")
	}
	if dt.IsColocated("unknown") {
		t.Error("IsColocated(unknown) = true, want false (not in explicit topology)")
	}
}

func TestDeploymentTopology_RemoteEndpoint_HitAndMiss(t *testing.T) {
	dt, err := newDeploymentTopology(DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote:    []RemoteCellEndpoint{{CellID: "cellC", Endpoint: "cell-c:8080"}},
	})
	if err != nil {
		t.Fatalf("newDeploymentTopology: %v", err)
	}

	// Hit
	ep, ok := dt.RemoteEndpoint("cellC")
	if !ok {
		t.Error("RemoteEndpoint(cellC): got miss, want hit")
	}
	if ep != "cell-c:8080" {
		t.Errorf("RemoteEndpoint(cellC) = %q, want %q", ep, "cell-c:8080")
	}

	// Miss: colocated cell
	_, ok = dt.RemoteEndpoint("cellA")
	if ok {
		t.Error("RemoteEndpoint(cellA): got hit, want miss (it is colocated)")
	}

	// Miss: unknown cell
	_, ok = dt.RemoteEndpoint("cellUnknown")
	if ok {
		t.Error("RemoteEndpoint(cellUnknown): got hit, want miss")
	}
}

// ---------------------------------------------------------------------------
// phase0 fail-fast: bad DeploymentTopologySpec triggers error in Run / phase0
// ---------------------------------------------------------------------------

// TestPhase0_RejectsInvalidDeploymentTopology verifies that when an invalid
// DeploymentTopologySpec is passed via WithDeploymentTopology, phase0ValidateOptions
// returns an error before any side effects start.
func TestPhase0_RejectsInvalidDeploymentTopology(t *testing.T) {
	// Mutual exclusion: same cellID in both colocated and remote.
	badSpec := DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote:    []RemoteCellEndpoint{{CellID: "cellA", Endpoint: "cell-a:8080"}},
	}
	b := New(
		clock.Real(),
		WithDeploymentTopology(badSpec),
		// Provide minimum viable listeners so phase0 does not fail on unrelated checks
		// before reaching validateDeploymentTopology.
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err := b.phase0ValidateOptions()
	if err == nil {
		t.Fatal("phase0ValidateOptions: expected error for invalid DeploymentTopologySpec, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error is not *errcode.Error: %T %v", err, err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("error code = %s, want %s", ec.Code, errcode.ErrValidationFailed)
	}
}

// TestPhase0_AcceptsValidDeploymentTopology verifies the happy path: a valid
// DeploymentTopologySpec does not cause phase0 to fail.
func TestPhase0_AcceptsValidDeploymentTopology(t *testing.T) {
	spec := DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote:    []RemoteCellEndpoint{{CellID: "cellB", Endpoint: "cell-b:9090"}},
	}
	b := New(
		clock.Real(),
		WithDeploymentTopology(spec),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err := b.phase0ValidateOptions()
	if err != nil {
		t.Fatalf("phase0ValidateOptions: unexpected error for valid DeploymentTopologySpec: %v", err)
	}

	// After phase0, b.deploymentTopology should be resolved and queryable.
	if !b.deploymentTopology.IsColocated("cellA") {
		t.Error("after phase0: IsColocated(cellA) = false, want true")
	}
	ep, ok := b.deploymentTopology.RemoteEndpoint("cellB")
	if !ok {
		t.Error("after phase0: RemoteEndpoint(cellB) = miss, want hit")
	}
	if ep != "cell-b:9090" {
		t.Errorf("after phase0: RemoteEndpoint(cellB) = %q, want %q", ep, "cell-b:9090")
	}
}

// TestPhase0_OmittedDeploymentTopology_AllColocated verifies that omitting
// WithDeploymentTopology leaves the zero DeploymentTopology (all cells
// co-located), which is the correct single-process default.
func TestPhase0_OmittedDeploymentTopology_AllColocated(t *testing.T) {
	b := New(
		clock.Real(),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err := b.phase0ValidateOptions()
	if err != nil {
		t.Fatalf("phase0ValidateOptions: unexpected error: %v", err)
	}

	// Zero topology: all cells colocated, no remotes.
	if !b.deploymentTopology.IsColocated("anyCellID") {
		t.Error("zero DeploymentTopology.IsColocated(any) = false, want true")
	}
	_, ok := b.deploymentTopology.RemoteEndpoint("anyCellID")
	if ok {
		t.Error("zero DeploymentTopology.RemoteEndpoint(any) = hit, want miss")
	}
}
