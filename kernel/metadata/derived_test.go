package metadata_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// makeProject builds a minimal ProjectMeta with the given cell IDs.
func makeProject(cellIDs ...string) *metadata.ProjectMeta {
	cells := make(map[string]*metadata.CellMeta, len(cellIDs))
	for _, id := range cellIDs {
		cells[id] = &metadata.CellMeta{ID: id}
	}
	return &metadata.ProjectMeta{
		Cells:      cells,
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func TestDeriveCellWireSummaries_Empty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project *metadata.ProjectMeta
		bundles map[string]metadata.CellWireBundle
	}{
		{
			name:    "nil project",
			project: nil,
			bundles: map[string]metadata.CellWireBundle{"a": {}},
		},
		{
			name:    "nil bundles",
			project: makeProject("accesscore"),
			bundles: nil,
		},
		{
			name:    "empty bundles map",
			project: makeProject("accesscore"),
			bundles: map[string]metadata.CellWireBundle{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.DeriveCellWireSummaries(tc.project, tc.bundles)
			if got == nil {
				t.Error("DeriveCellWireSummaries must return non-nil slice (JSON [] not null)")
			}
			if len(got) != 0 {
				t.Errorf("expected empty slice, got %d elements", len(got))
			}
		})
	}
}

func TestDeriveCellWireSummaries_OmitsCellsNotInProject(t *testing.T) {
	t.Parallel()

	project := makeProject("accesscore")
	bundles := map[string]metadata.CellWireBundle{
		"accesscore": {
			Listeners: []metadata.WireBundleListener{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
		},
		"ghost": {
			// "ghost" is not in project.Cells → must be omitted
			Listeners: []metadata.WireBundleListener{{Ref: "cell.PrimaryListener"}},
		},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(got), got)
	}
	if got[0].CellID != "accesscore" {
		t.Errorf("expected cellId=accesscore, got %q", got[0].CellID)
	}
}

func TestDeriveCellWireSummaries_Sorted(t *testing.T) {
	t.Parallel()

	project := makeProject("zzz", "aaa", "mmm")
	bundles := map[string]metadata.CellWireBundle{
		"zzz": {},
		"aaa": {},
		"mmm": {},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	want := []string{"aaa", "mmm", "zzz"}
	for i, w := range want {
		if got[i].CellID != w {
			t.Errorf("position %d: got cellId=%q, want %q", i, got[i].CellID, w)
		}
	}
}

func TestDeriveCellWireSummaries_ListenersProjected(t *testing.T) {
	t.Parallel()

	project := makeProject("accesscore")
	bundles := map[string]metadata.CellWireBundle{
		"accesscore": {
			Listeners: []metadata.WireBundleListener{
				{Ref: "cell.PrimaryListener", Prefix: "/api/v1/access"},
				{Ref: "cell.InternalListener", Prefix: "/internal/v1/access"},
			},
		},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	s := got[0]
	if len(s.Listeners) != 2 {
		t.Fatalf("expected 2 listeners, got %d", len(s.Listeners))
	}
	if s.Listeners[0].Ref != "cell.PrimaryListener" {
		t.Errorf("listener[0].ref = %q, want cell.PrimaryListener", s.Listeners[0].Ref)
	}
	if s.Listeners[0].Prefix != "/api/v1/access" {
		t.Errorf("listener[0].prefix = %q, want /api/v1/access", s.Listeners[0].Prefix)
	}
	if s.Listeners[1].Ref != "cell.InternalListener" {
		t.Errorf("listener[1].ref = %q, want cell.InternalListener", s.Listeners[1].Ref)
	}
}

func TestDeriveCellWireSummaries_RoutesProjected(t *testing.T) {
	t.Parallel()

	project := makeProject("accesscore")
	bundles := map[string]metadata.CellWireBundle{
		"accesscore": {
			Routes: []metadata.WireBundleRoute{
				{Slice: "accesscore/sessionlogin", Listener: "cell.PrimaryListener", SubPath: "/sessions/login", Method: "RegisterRoutes"},
				{Slice: "accesscore/sessionlogout", Listener: "cell.PrimaryListener", SubPath: "/sessions/logout"},
			},
		},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	s := got[0]
	if len(s.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d: %+v", len(s.Routes), s.Routes)
	}
	if s.Routes[0].Slice != "accesscore/sessionlogin" {
		t.Errorf("route[0].slice = %q", s.Routes[0].Slice)
	}
	if s.Routes[0].Method != "RegisterRoutes" {
		t.Errorf("route[0].method = %q, want RegisterRoutes", s.Routes[0].Method)
	}
	if s.Routes[1].Slice != "accesscore/sessionlogout" {
		t.Errorf("route[1].slice = %q", s.Routes[1].Slice)
	}
	if s.Routes[1].Method != "" {
		t.Errorf("route[1].method = %q, want empty (omitted)", s.Routes[1].Method)
	}
}

func TestDeriveCellWireSummaries_SubscribesProjected(t *testing.T) {
	t.Parallel()

	project := makeProject("auditcore")
	bundles := map[string]metadata.CellWireBundle{
		"auditcore": {
			Subscribes: []metadata.WireBundleSubscribe{
				{Slice: "auditcore/auditappend", Topic: "event.session.created.v1", Handler: "HandleSessionCreated", Group: "auditcore"},
			},
		},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	s := got[0]
	if len(s.Subscribes) != 1 {
		t.Fatalf("expected 1 subscribe, got %d", len(s.Subscribes))
	}
	sub := s.Subscribes[0]
	if sub.Slice != "auditcore/auditappend" {
		t.Errorf("sub.slice = %q", sub.Slice)
	}
	if sub.Topic != "event.session.created.v1" {
		t.Errorf("sub.topic = %q", sub.Topic)
	}
	if sub.Handler != "HandleSessionCreated" {
		t.Errorf("sub.handler = %q", sub.Handler)
	}
	if sub.Group != "auditcore" {
		t.Errorf("sub.group = %q", sub.Group)
	}
}

func TestDeriveCellWireSummaries_EmptyBundleYieldsEmptySlices(t *testing.T) {
	t.Parallel()

	// A cell with an empty bundle should appear in the result with empty (non-nil) slices.
	project := makeProject("configcore")
	bundles := map[string]metadata.CellWireBundle{
		"configcore": {},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	s := got[0]
	if s.CellID != "configcore" {
		t.Errorf("cellId = %q, want configcore", s.CellID)
	}
	if s.Listeners == nil {
		t.Error("Listeners must be non-nil for empty bundle (JSON [] not null)")
	}
	if s.Routes == nil {
		t.Error("Routes must be non-nil for empty bundle (JSON [] not null)")
	}
	if s.Subscribes == nil {
		t.Error("Subscribes must be non-nil for empty bundle (JSON [] not null)")
	}
}

func TestDeriveCellWireSummaries_MultiCell_FullBundle(t *testing.T) {
	t.Parallel()

	project := makeProject("accesscore", "auditcore")
	bundles := map[string]metadata.CellWireBundle{
		"accesscore": {
			Listeners: []metadata.WireBundleListener{{Ref: "cell.PrimaryListener", Prefix: "/api/v1/access"}},
			Routes:    []metadata.WireBundleRoute{{Slice: "accesscore/sessionlogin", Listener: "cell.PrimaryListener", SubPath: "/sessions/login"}},
		},
		"auditcore": {
			Subscribes: []metadata.WireBundleSubscribe{
				{Slice: "auditcore/auditappend", Topic: "event.session.created.v1", Handler: "Handle", Group: "auditcore"},
			},
		},
	}

	got := metadata.DeriveCellWireSummaries(project, bundles)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	// Sorted: accesscore < auditcore
	if got[0].CellID != "accesscore" {
		t.Errorf("got[0].cellId = %q, want accesscore", got[0].CellID)
	}
	if got[1].CellID != "auditcore" {
		t.Errorf("got[1].cellId = %q, want auditcore", got[1].CellID)
	}
	if len(got[0].Listeners) != 1 {
		t.Errorf("accesscore listeners count = %d, want 1", len(got[0].Listeners))
	}
	if len(got[1].Subscribes) != 1 {
		t.Errorf("auditcore subscribes count = %d, want 1", len(got[1].Subscribes))
	}
}
