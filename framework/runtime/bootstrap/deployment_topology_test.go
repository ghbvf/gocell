package bootstrap

// deployment_topology_test.go — tests for DeploymentTopology WriteOnce API.
//
// INVARIANT: DEPLOYMENT-TOPOLOGY-SEALED-FIELD-FROZEN-01

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
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
		// F8: whitespace-only endpoint
		{
			name: "whitespace-only endpoint",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{{CellID: "cellA", Endpoint: "   "}},
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
// A split topology requires postgres storage + non-nil publisher/subscriber
// (broker-mandatory gate, F1); inject both so the gate accepts it.
func TestPhase0_AcceptsValidDeploymentTopology(t *testing.T) {
	spec := DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote:    []RemoteCellEndpoint{{CellID: "cellB", Endpoint: "cell-b:9090"}},
	}
	postgresTopo, err := NewTopology("real", "postgres", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}
	brokerBus := eventbus.New(clock.Real())
	b := New(
		clock.Real(),
		WithDeploymentTopology(spec),
		WithControlPlaneTopology(postgresTopo),
		WithPublisher(brokerBus),
		WithSubscriber(brokerBus),
		WithEventTransportKind(RealBrokerEventTransport()),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err = b.phase0ValidateOptions()
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

// TestBootstrap_DeploymentTopologyGetter_BeforePhase0 verifies that the
// Bootstrap.DeploymentTopology() getter returns the zero value (all-colocated)
// before phase0 runs, and the sealed value after (F4).
// A split topology requires postgres storage + non-nil publisher/subscriber
// (broker-mandatory gate, F1); inject both so the gate accepts the split spec.
func TestBootstrap_DeploymentTopologyGetter_BeforePhase0(t *testing.T) {
	postgresTopo, err := NewTopology("real", "postgres", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}
	brokerBus := eventbus.New(clock.Real())
	b := New(
		clock.Real(),
		WithDeploymentTopology(DeploymentTopologySpec{
			Colocated: []string{"cellA"},
			Remote:    []RemoteCellEndpoint{{CellID: "cellB", Endpoint: "cell-b:9090"}},
		}),
		WithControlPlaneTopology(postgresTopo),
		WithPublisher(brokerBus),
		WithSubscriber(brokerBus),
		WithEventTransportKind(RealBrokerEventTransport()),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	// Before phase0: getter returns zero value (all-colocated).
	preDT := b.DeploymentTopology()
	if !preDT.IsColocated("anyCellID") {
		t.Error("before phase0: DeploymentTopology().IsColocated(any) should be true (zero = all-colocated)")
	}
	_, ok := preDT.RemoteEndpoint("cellB")
	if ok {
		t.Error("before phase0: DeploymentTopology().RemoteEndpoint(cellB) should miss (zero = no remotes)")
	}

	// After phase0: getter returns the sealed value.
	if err := b.phase0ValidateOptions(); err != nil {
		t.Fatalf("phase0ValidateOptions: unexpected error: %v", err)
	}
	dt := b.DeploymentTopology()
	if !dt.IsColocated("cellA") {
		t.Error("after phase0: DeploymentTopology().IsColocated(cellA) = false, want true")
	}
	ep, ok := dt.RemoteEndpoint("cellB")
	if !ok {
		t.Error("after phase0: DeploymentTopology().RemoteEndpoint(cellB) = miss, want hit")
	}
	if ep != "cell-b:9090" {
		t.Errorf("after phase0: DeploymentTopology().RemoteEndpoint(cellB) = %q, want %q", ep, "cell-b:9090")
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

// ---------------------------------------------------------------------------
// HasRemoteCells predicate
// ---------------------------------------------------------------------------

// TestDeploymentTopologyHasRemoteCells verifies the HasRemoteCells predicate
// used by the phase0 broker-mandatory gate (validateSplitTopologyBroker).
func TestDeploymentTopologyHasRemoteCells(t *testing.T) {
	cases := []struct {
		name string
		spec DeploymentTopologySpec
		want bool
	}{
		{
			name: "zero value (no topology declared) → false",
			spec: DeploymentTopologySpec{},
			want: false,
		},
		{
			name: "only colocated cells → false",
			spec: DeploymentTopologySpec{
				Colocated: []string{"cellA", "cellB"},
			},
			want: false,
		},
		{
			name: "at least one remote cell → true",
			spec: DeploymentTopologySpec{
				Remote: []RemoteCellEndpoint{
					{CellID: "cellRemote", Endpoint: "cell-remote:8080"},
				},
			},
			want: true,
		},
		{
			name: "mixed colocated + remote → true",
			spec: DeploymentTopologySpec{
				Colocated: []string{"cellA"},
				Remote: []RemoteCellEndpoint{
					{CellID: "cellB", Endpoint: "cell-b:9090"},
				},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dt, err := newDeploymentTopology(tc.spec)
			if err != nil {
				t.Fatalf("newDeploymentTopology: unexpected error: %v", err)
			}
			if got := dt.HasRemoteCells(); got != tc.want {
				t.Errorf("HasRemoteCells() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeploymentTopologyHasNonLoopbackRemoteCells(t *testing.T) {
	cases := []struct {
		name string
		spec DeploymentTopologySpec
		want bool
	}{
		{name: "zero value → false", spec: DeploymentTopologySpec{}, want: false},
		{
			name: "only colocated → false",
			spec: DeploymentTopologySpec{Colocated: []string{"cellA"}},
			want: false,
		},
		{
			name: "loopback ipv4 remote → false (local dev split)",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{{CellID: "cellB", Endpoint: "127.0.0.1:9090"}}},
			want: false,
		},
		{
			name: "loopback localhost URL remote → false",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{{CellID: "cellB", Endpoint: "https://localhost:8443"}}},
			want: false,
		},
		{
			name: "non-loopback dns remote → true (network boundary)",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{{CellID: "cellB", Endpoint: "https://cell-b.svc:8443"}}},
			want: true,
		},
		{
			name: "mixed loopback + non-loopback → true (any non-loopback triggers)",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{
				{CellID: "cellB", Endpoint: "127.0.0.1:9090"},
				{CellID: "cellC", Endpoint: "cell-c:9090"},
			}},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dt, err := newDeploymentTopology(tc.spec)
			if err != nil {
				t.Fatalf("newDeploymentTopology: unexpected error: %v", err)
			}
			if got := dt.HasNonLoopbackRemoteCells(); got != tc.want {
				t.Errorf("HasNonLoopbackRemoteCells() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeploymentTopologySharedNonLoopbackRemoteEndpoint(t *testing.T) {
	cases := []struct {
		name      string
		spec      DeploymentTopologySpec
		wantFound bool
		wantEP    string
		wantCells []string
	}{
		{name: "no remote → none", spec: DeploymentTopologySpec{}, wantFound: false},
		{
			name: "unique non-loopback endpoints → none",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{
				{CellID: "cellA", Endpoint: "https://a.svc:8443"},
				{CellID: "cellB", Endpoint: "https://b.svc:8443"},
			}},
			wantFound: false,
		},
		{
			name: "two cells share a non-loopback endpoint → found",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{
				{CellID: "cellB", Endpoint: "https://shared.svc:8443"},
				{CellID: "cellC", Endpoint: "https://shared.svc:8443"},
			}},
			wantFound: true, wantEP: "https://shared.svc:8443", wantCells: []string{"cellB", "cellC"},
		},
		{
			name: "shared LOOPBACK endpoint is exempt → none",
			spec: DeploymentTopologySpec{Remote: []RemoteCellEndpoint{
				{CellID: "cellB", Endpoint: "127.0.0.1:9090"},
				{CellID: "cellC", Endpoint: "127.0.0.1:9090"},
			}},
			wantFound: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSharedNonLoopbackRemoteEndpoint(t, tc.spec, tc.wantFound, tc.wantEP, tc.wantCells)
		})
	}
}

// assertSharedNonLoopbackRemoteEndpoint resolves spec and checks
// SharedNonLoopbackRemoteEndpoint against the expectations. Extracted from the
// table loop so neither function exceeds cognitive-complexity limits.
func assertSharedNonLoopbackRemoteEndpoint(t *testing.T, spec DeploymentTopologySpec, wantFound bool, wantEP string, wantCells []string) {
	t.Helper()
	dt, err := newDeploymentTopology(spec)
	if err != nil {
		t.Fatalf("newDeploymentTopology: %v", err)
	}
	ep, cells, found := dt.SharedNonLoopbackRemoteEndpoint()
	if found != wantFound {
		t.Fatalf("found = %v, want %v", found, wantFound)
	}
	if !found {
		return
	}
	if ep != wantEP {
		t.Errorf("endpoint = %q, want %q", ep, wantEP)
	}
	if strings.Join(cells, ",") != strings.Join(wantCells, ",") {
		t.Errorf("cells = %v, want %v", cells, wantCells)
	}
}

// ---------------------------------------------------------------------------
// validateSplitTopologyBroker — phase0 broker-mandatory gate
// ---------------------------------------------------------------------------

// TestValidateSplitTopologyBroker exercises the phase0 broker-mandatory gate
// (validateSplitTopologyBroker) directly, without starting a full Bootstrap.
// Since #2211 the gate keys off the sealed EventTransportKind fact (minted only
// by eventtransport.Resolve) rather than the StorageBackend()=="postgres" proxy.
// The gate must:
//   - reject split topology (≥1 remote) + in-memory kind
//   - reject split topology + UNSET kind (composition root forgot the option →
//     fail-closed, same posture as a forgotten publisher/subscriber)
//   - reject split topology + real-broker kind but nil/typed-nil publisher or
//     subscriber (phase2 would degrade to in-memory bus — same security gap)
//   - accept split topology + real-broker kind + non-nil publisher + subscriber
//   - accept colocated / zero topology regardless of kind (gate does not fire)
func TestValidateSplitTopologyBroker(t *testing.T) {
	splitSpec := DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote: []RemoteCellEndpoint{
			{CellID: "cellB", Endpoint: "cell-b:9090"},
		},
	}
	colocatedSpec := DeploymentTopologySpec{
		Colocated: []string{"cellA", "cellB"},
	}

	splitDT, err := newDeploymentTopology(splitSpec)
	if err != nil {
		t.Fatalf("newDeploymentTopology(split): %v", err)
	}
	colocatedDT, err := newDeploymentTopology(colocatedSpec)
	if err != nil {
		t.Fatalf("newDeploymentTopology(colocated): %v", err)
	}

	// nonNilBus is used for the GREEN cases that require non-nil publisher/subscriber.
	nonNilBus := eventbus.New(clock.Real())

	// typedNilPub / typedNilSub are typed-nil interface values (non-nil interface
	// header, nil concrete pointer). A bare == nil check would MISS them; the gate
	// uses validation.IsNilInterface so both publisher and subscriber are correctly
	// rejected (#2188 review F3). Both directions are covered to lock the symmetric
	// IsNilInterface check on each sink.
	var typedNilPub outbox.Publisher = (*eventbus.InMemoryEventBus)(nil)
	var typedNilSub outbox.Subscriber = (*eventbus.InMemoryEventBus)(nil)

	cases := []struct {
		name               string
		deploymentTopology DeploymentTopology
		kind               EventTransportKind
		publisher          outbox.Publisher
		subscriber         outbox.Subscriber
		wantErr            bool
		wantErrCode        errcode.Code
	}{
		{
			name:               "RED: split topology + in-memory kind → rejected",
			deploymentTopology: splitDT,
			kind:               InMemoryEventTransport(),
			publisher:          nonNilBus,
			subscriber:         nonNilBus,
			wantErr:            true,
			wantErrCode:        errcode.ErrValidationFailed,
		},
		{
			// Composition root forgot WithEventTransportKind → zero value (unset).
			// IsRealBroker()==false → fail-closed, exactly like a forgotten broker.
			name:               "RED: split topology + UNSET kind → rejected (fail-closed)",
			deploymentTopology: splitDT,
			kind:               EventTransportKind{}, // unset
			publisher:          nonNilBus,
			subscriber:         nonNilBus,
			wantErr:            true,
			wantErrCode:        errcode.ErrValidationFailed,
		},
		{
			// real-broker kind but nil publisher → rejected. phase2InitPubSub falls
			// back to in-memory bus when publisher==nil, so this stays fail-closed
			// (independent invariant: a real-broker kind with a nil sink is broken wiring).
			name:               "RED: split topology + real-broker kind + nil publisher → rejected (nil guard)",
			deploymentTopology: splitDT,
			kind:               RealBrokerEventTransport(),
			publisher:          nil,
			subscriber:         nonNilBus,
			wantErr:            true,
			wantErrCode:        errcode.ErrValidationFailed,
		},
		{
			name:               "RED: split topology + real-broker kind + nil subscriber → rejected (nil guard)",
			deploymentTopology: splitDT,
			kind:               RealBrokerEventTransport(),
			publisher:          nonNilBus,
			subscriber:         nil,
			wantErr:            true,
			wantErrCode:        errcode.ErrValidationFailed,
		},
		{
			// F3: typed-nil publisher (non-nil interface, nil concrete pointer) must
			// be rejected — a bare == nil check would let it pass and phase2 would
			// degrade to the in-memory bus. validation.IsNilInterface closes it.
			name:               "RED: split topology + real-broker kind + typed-nil publisher → rejected (F3 typed-nil)",
			deploymentTopology: splitDT,
			kind:               RealBrokerEventTransport(),
			publisher:          typedNilPub,
			subscriber:         nonNilBus,
			wantErr:            true,
			wantErrCode:        errcode.ErrValidationFailed,
		},
		{
			// Symmetric to the publisher case: a typed-nil subscriber must also be
			// rejected (the gate's IsNilInterface check is applied to both sinks).
			name:               "RED: split topology + real-broker kind + typed-nil subscriber → rejected (F3 typed-nil)",
			deploymentTopology: splitDT,
			kind:               RealBrokerEventTransport(),
			publisher:          nonNilBus,
			subscriber:         typedNilSub,
			wantErr:            true,
			wantErrCode:        errcode.ErrValidationFailed,
		},
		{
			name:               "GREEN: split topology + real-broker kind + non-nil pub/sub → accepted",
			deploymentTopology: splitDT,
			kind:               RealBrokerEventTransport(),
			publisher:          nonNilBus,
			subscriber:         nonNilBus,
			wantErr:            false,
		},
		{
			name:               "GREEN: colocated topology + in-memory kind → accepted (no remote cells)",
			deploymentTopology: colocatedDT,
			kind:               InMemoryEventTransport(),
			wantErr:            false,
		},
		{
			name:               "GREEN: zero deployment topology + unset kind → accepted (all-colocated default)",
			deploymentTopology: DeploymentTopology{},
			kind:               EventTransportKind{},
			wantErr:            false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bootstrap{
				deploymentTopology: tc.deploymentTopology,
				eventTransportKind: tc.kind,
				publisher:          tc.publisher,
				subscriber:         tc.subscriber,
			}
			err := b.validateSplitTopologyBroker()
			if tc.wantErr {
				if err == nil {
					t.Fatal("validateSplitTopologyBroker: expected error, got nil")
				}
				var ec *errcode.Error
				if !errors.As(err, &ec) {
					t.Fatalf("error is not *errcode.Error: %T %v", err, err)
				}
				if ec.Code != tc.wantErrCode {
					t.Errorf("error code = %s, want %s", ec.Code, tc.wantErrCode)
				}
			} else if err != nil {
				t.Errorf("validateSplitTopologyBroker: unexpected error: %v", err)
			}
		})
	}
}

// TestPhase0_RejectsSplitTopologyWithoutBrokerKind verifies that phase0 end-to-end
// rejects a split topology when no real-broker EventTransportKind is declared.
// The composition root here omits WithEventTransportKind, so the kind is the zero
// value (unset) → IsRealBroker()==false → fail-closed. Since #2211 the rejection
// is driven by the sealed kind, NOT by the (now gate-irrelevant) controlPlaneTopology
// storage backend — the in-memory bus can no longer be reached in a split topology.
func TestPhase0_RejectsSplitTopologyWithoutBrokerKind(t *testing.T) {
	splitSpec := DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote: []RemoteCellEndpoint{
			{CellID: "cellB", Endpoint: "cell-b:9090"},
		},
	}
	memoryTopo, err := NewTopology("", "memory", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}

	b := New(
		clock.Real(),
		WithDeploymentTopology(splitSpec),
		WithControlPlaneTopology(memoryTopo),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err = b.phase0ValidateOptions()
	if err == nil {
		t.Fatal("phase0ValidateOptions: expected error for split topology without a real-broker kind, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error is not *errcode.Error: %T %v", err, err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("error code = %s, want %s", ec.Code, errcode.ErrValidationFailed)
	}
	if !strings.Contains(ec.Message, "in-memory") {
		t.Errorf("error message %q should mention in-memory bus", ec.Message)
	}
}

// TestPhase0_AcceptsSplitTopologyWithPostgres verifies that phase0 accepts a
// split topology when a real-broker EventTransportKind + non-nil publisher/subscriber
// are injected (broker-mandatory gate). Note: brokerBus is an in-memory eventbus
// used only to satisfy the non-nil sink check — the gate validates the sealed Kind
// fact, NOT the bus implementation. The honest pairing (a real kind ⇒ a real bus)
// is the composition root's job; in production COREBUNDLE-EVENTBUS-FUNNEL-01 makes
// the dishonest pairing import-unexpressible, so this unit-test shortcut cannot leak.
func TestPhase0_AcceptsSplitTopologyWithPostgres(t *testing.T) {
	splitSpec := DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote: []RemoteCellEndpoint{
			{CellID: "cellB", Endpoint: "cell-b:9090"},
		},
	}
	postgresTopo, err := NewTopology("real", "postgres", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}
	brokerBus := eventbus.New(clock.Real())

	b := New(
		clock.Real(),
		WithDeploymentTopology(splitSpec),
		WithControlPlaneTopology(postgresTopo),
		WithPublisher(brokerBus),
		WithSubscriber(brokerBus),
		WithEventTransportKind(RealBrokerEventTransport()),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}),
	)

	err = b.phase0ValidateOptions()
	if err != nil {
		t.Fatalf("phase0ValidateOptions: unexpected error for split topology + postgres + non-nil pub/sub: %v", err)
	}
}
