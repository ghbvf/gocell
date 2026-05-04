package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// fixtureProject builds a minimal in-memory ProjectMeta with one cell and
// configurable slices/contracts so test cases can vary attributes without
// touching the filesystem.
func fixtureProject(cell *metadata.CellMeta, slices []*metadata.SliceMeta, contracts []*metadata.ContractMeta) *metadata.ProjectMeta {
	p := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
	}
	if cell != nil {
		p.Cells[cell.ID] = cell
	}
	for _, s := range slices {
		p.Slices[s.BelongsToCell+"/"+s.ID] = s
	}
	for _, c := range contracts {
		p.Contracts[c.ID] = c
	}
	return p
}

// bundleWithListener returns a WireBundle with a single listener declaration.
func bundleWithListener(ref, prefix string) markergen.WireBundle {
	return markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{{Ref: ref, Prefix: prefix}},
	}
}

func TestBuildCellSpec_HappyPath_OneListenerOneSubRoute(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "demo",
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: "Demo",
	}
	slc := &metadata.SliceMeta{
		ID:            "alpha",
		BelongsToCell: "demo",
		Dir:           "alpha",
		File:          "cells/demo/slices/alpha/slice.yaml",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
		Routes: []markergen.RouteSpec{
			{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/widgets", HandlerField: "alphaHandler"},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if spec.StructName != "Demo" {
		t.Errorf("StructName = %q", spec.StructName)
	}
	if len(spec.RouteGroups) != 1 {
		t.Fatalf("RouteGroups len = %d", len(spec.RouteGroups))
	}
	rg := spec.RouteGroups[0]
	if rg.ListenerConst != "cell.PrimaryListener" || rg.Prefix != "/api/v1" {
		t.Errorf("listener/prefix = %q/%q", rg.ListenerConst, rg.Prefix)
	}
	if len(rg.SubRoutes) != 1 || rg.SubRoutes[0].SubPath != "/widgets" {
		t.Fatalf("SubRoutes = %#v", rg.SubRoutes)
	}
	if len(rg.SubRoutes[0].Mounts) != 1 || rg.SubRoutes[0].Mounts[0].HandlerField != "alphaHandler" {
		t.Errorf("mount = %#v", rg.SubRoutes[0].Mounts)
	}
	if rg.SubRoutes[0].Mounts[0].Method != "RegisterRoutes" {
		t.Errorf("default Method should be RegisterRoutes, got %q", rg.SubRoutes[0].Mounts[0].Method)
	}
}

func TestBuildCellSpec_GroupsTwoSlicesUnderSameSubPath(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slcA := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	slcB := &metadata.SliceMeta{ID: "beta", BelongsToCell: "demo", Dir: "beta", File: "cells/demo/slices/beta/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slcA, slcB}, nil)
	bundle := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
		Routes: []markergen.RouteSpec{
			{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "createH"},
			{Slice: "beta", Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "queryH"},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	rg := spec.RouteGroups[0]
	if len(rg.SubRoutes) != 1 || rg.SubRoutes[0].SubPath != "/items" {
		t.Fatalf("expected one /items subroute, got %#v", rg.SubRoutes)
	}
	mounts := rg.SubRoutes[0].Mounts
	if len(mounts) != 2 || mounts[0].HandlerField != "createH" || mounts[1].HandlerField != "queryH" {
		t.Errorf("mounts ordering broken: %#v", mounts)
	}
}

func TestBuildCellSpec_TwoListenersDeterministicOrder(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{
			{Ref: "cell.PrimaryListener", Prefix: "/api/v1"},
			{Ref: "cell.InternalListener", Prefix: "/internal/v1"},
		},
		Routes: []markergen.RouteSpec{
			{Slice: "alpha", Listener: "cell.InternalListener", SubPath: "/admin", HandlerField: "adminH"},
			{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "itemH"},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.RouteGroups) != 2 {
		t.Fatalf("RouteGroups len = %d", len(spec.RouteGroups))
	}
	if spec.RouteGroups[0].ListenerConst != "cell.PrimaryListener" {
		t.Errorf("first listener should match declaration order, got %q", spec.RouteGroups[0].ListenerConst)
	}
	if spec.RouteGroups[1].ListenerConst != "cell.InternalListener" {
		t.Errorf("second listener should be internal, got %q", spec.RouteGroups[1].ListenerConst)
	}
}

func TestBuildCellSpec_EmptySubPathMountsDirectlyOnPrefix(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{{Ref: "cell.InternalListener", Prefix: "/internal/v1/admin"}},
		Routes:    []markergen.RouteSpec{{Slice: "alpha", Listener: "cell.InternalListener", SubPath: "", HandlerField: "adminH"}},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if spec.RouteGroups[0].SubRoutes[0].SubPath != "" {
		t.Errorf("empty subPath should propagate; got %q", spec.RouteGroups[0].SubRoutes[0].SubPath)
	}
}

func TestBuildCellSpec_SubscribesProduceSpecVarsAndExpr(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	contract := &metadata.ContractMeta{ID: "event.foo.bar.v1", Kind: "event"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "event.foo.bar.v1", SliceField: "barSvc", Handler: "HandleBar", Group: "demo"},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Subscriptions) != 1 {
		t.Fatalf("Subscriptions len = %d", len(spec.Subscriptions))
	}
	sub := spec.Subscriptions[0]
	if sub.SpecVarName != "specEventFooBar" {
		t.Errorf("SpecVarName = %q want specEventFooBar", sub.SpecVarName)
	}
	if sub.HandlerExpr != "c.barSvc.HandleBar" {
		t.Errorf("HandlerExpr = %q", sub.HandlerExpr)
	}
	if sub.SliceID != "subs" {
		t.Errorf("SliceID = %q", sub.SliceID)
	}
}

func TestBuildCellSpec_ConsumerGroupOverride(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "event.foo.bar.v1", SliceField: "barSvc", Handler: "HandleBar", Group: "demo-fanout"},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if spec.Subscriptions[0].ConsumerGroup != "demo-fanout" {
		t.Errorf("ConsumerGroup override lost: got %q", spec.Subscriptions[0].ConsumerGroup)
	}
}

func TestBuildCellSpec_NilProjectFails(t *testing.T) {
	t.Parallel()
	if _, err := BuildCellSpec(nil, "x", markergen.WireBundle{}); err == nil {
		t.Fatal("expected error for nil project")
	}
}

func TestBuildCellSpec_UnknownCellFails(t *testing.T) {
	t.Parallel()
	p := fixtureProject(nil, nil, nil)
	_, err := BuildCellSpec(p, "ghost", markergen.WireBundle{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestBuildCellSpec_MissingGoStructNameFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml"}
	p := fixtureProject(cell, nil, nil)
	_, err := BuildCellSpec(p, "demo", markergen.WireBundle{})
	if err == nil || !strings.Contains(err.Error(), "goStructName") {
		t.Fatalf("expected goStructName error, got %v", err)
	}
}

func TestBuildCellSpec_DuplicateListenerFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	p := fixtureProject(cell, nil, nil)
	bundle := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{
			{Ref: "cell.PrimaryListener", Prefix: "/api/v1"},
			{Ref: "cell.PrimaryListener", Prefix: "/api/v2"},
		},
	}
	_, err := BuildCellSpec(p, "demo", bundle)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("expected duplicate-listener error, got %v", err)
	}
}

func TestBuildCellSpec_RouteMountUndeclaredListenerFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		// No listeners declared — route references undeclared listener.
		Routes: []markergen.RouteSpec{
			{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/", HandlerField: "h"},
		},
	}
	_, err := BuildCellSpec(p, "demo", bundle)
	if err == nil || !strings.Contains(err.Error(), "undeclared listener") {
		t.Fatalf("expected undeclared-listener error, got %v", err)
	}
}

func TestBuildCellSpec_UnknownContractFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "ghost.event.v1", SliceField: "x", Handler: "Y", Group: "demo"},
		},
	}
	_, err := BuildCellSpec(p, "demo", bundle)
	if err == nil || !strings.Contains(err.Error(), "unknown contract") {
		t.Fatalf("expected unknown-contract error, got %v", err)
	}
}

func TestBuildCellSpec_NonEventContractRejected(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "http.users.v1", Kind: "http"}})
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "http.users.v1", SliceField: "x", Handler: "Y", Group: "demo"},
		},
	}
	_, err := BuildCellSpec(p, "demo", bundle)
	if err == nil || !strings.Contains(err.Error(), "non-event") {
		t.Fatalf("expected non-event-contract error, got %v", err)
	}
}

func TestBuildCellSpec_NoListenersWithRouteMountFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	// Bundle has a route but no listener declared.
	bundle := markergen.WireBundle{
		Routes: []markergen.RouteSpec{
			{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/x", HandlerField: "h"},
		},
	}
	_, err := BuildCellSpec(p, "demo", bundle)
	if err == nil || !strings.Contains(err.Error(), "undeclared listener") {
		t.Fatalf("expected undeclared-listener error, got %v", err)
	}
}

func TestBuildSliceSpec_NoSubscribesReturnsNil(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildSliceSpec(p, "demo", "alpha", markergen.WireBundle{})
	if err != nil {
		t.Fatalf("BuildSliceSpec: %v", err)
	}
	if spec != nil {
		t.Errorf("expected nil spec for slice without subscribes")
	}
}

func TestBuildSliceSpec_SubscribesProduceHandlerInterface(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "event.b.v1", SliceField: "svc", Handler: "HandleB", Group: "demo"},
			{Slice: "subs", Topic: "event.a.v1", SliceField: "svc", Handler: "HandleA", Group: "demo"},
		},
	}

	spec, err := BuildSliceSpec(p, "demo", "subs", bundle)
	if err != nil {
		t.Fatalf("BuildSliceSpec: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if len(spec.Handlers) != 2 {
		t.Fatalf("Handlers len = %d", len(spec.Handlers))
	}
	if spec.Handlers[0].MethodName != "HandleA" || spec.Handlers[1].MethodName != "HandleB" {
		t.Errorf("handler ordering not alphabetical: %+v", spec.Handlers)
	}
}

func TestBuildSliceSpec_SliceNotFoundFails(t *testing.T) {
	t.Parallel()
	p := fixtureProject(nil, nil, nil)
	_, err := BuildSliceSpec(p, "x", "y", markergen.WireBundle{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestSpecVarName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"event.config.entry-upserted.v1", "specEventConfigEntryUpserted"},
		{"event.role.assigned.v1", "specEventRoleAssigned"},
		{"event.audit.appended.v1", "specEventAuditAppended"},
		{"event.foo-bar.baz.v3", "specEventFooBarBaz"},
		{"event.no.version", "specEventNoVersion"},
		{"event.with-dash.v10", "specEventWithDash"},
	}
	for _, tc := range cases {
		got, err := specVarName(tc.in)
		if err != nil {
			t.Errorf("specVarName(%q) returned unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("specVarName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSpecVarName_VersionSubMinorRejected documents the strict reading of
// IMP-8: a contract id like "event.foo.v1.0" has segments
// ["event", "foo", "v1", "0"]. isVersionSegment only matches "v\d+" so neither
// "v1" nor "0" is stripped; the trailing "0" then fails the
// digit-leading-segment guard. This shape is a malformed contract id —
// versions in GoCell are single-token (v1, v2, v10), never v1.0.
func TestSpecVarName_VersionSubMinorRejected(t *testing.T) {
	t.Parallel()
	_, err := specVarName("event.foo.v1.0")
	if err == nil || !strings.Contains(err.Error(), "non-letter-leading segment") {
		t.Errorf("event.foo.v1.0 should be rejected as malformed (sub-minor versions not supported); got %v", err)
	}
}

// TestSpecVarName_DegenerateInputErrors verifies that a contract id whose
// only segment is a version tag (e.g. "v1") returns an error rather than
// silently producing the invalid identifier "spec". The YAML validator
// already rejects such ids; this is defense in depth.
func TestSpecVarName_DegenerateInputErrors(t *testing.T) {
	t.Parallel()
	got, err := specVarName("v1")
	if err == nil {
		t.Fatalf("expected error for degenerate contract id, got %q", got)
	}
	if !strings.Contains(err.Error(), "non-conforming spec var") {
		t.Errorf("error message missing 'non-conforming spec var': %v", err)
	}
}

// TestSpecVarName_DigitLeadingSegmentRejected verifies IMP-8: contract ids
// containing a digit-leading dotted segment (e.g. "event.123foo.v1")
// produce a non-conforming spec var ("specEvent123foo" — digit immediately
// after camel-case boundary) and must be rejected, not silently emitted.
func TestSpecVarName_DigitLeadingSegmentRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"event.123foo.v1",     // digit-leading middle segment
		"123foo.bar.v1",       // digit-leading first segment
		"event.foo.123bar.v1", // digit-leading later segment
	}
	for _, in := range cases {
		_, err := specVarName(in)
		if err == nil {
			t.Errorf("specVarName(%q) should reject digit-leading segment", in)
		}
	}
}

// TestBuildCellSpec_ConsumerGroupEmptyFallsBackToCellID verifies IMP-3:
// when the subscribe marker omits group, BuildCellSpec leaves the
// SubscriptionGenSpec.ConsumerGroup empty and the template uses
// CellGenSpec.ConsumerGroupDefault (which is the cell ID). The empty-value
// path is the common case and must remain explicitly tested.
func TestBuildCellSpec_ConsumerGroupEmptyFallsBackToCellID(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "event.foo.bar.v1", SliceField: "barSvc", Handler: "HandleBar", Group: ""},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if spec.ConsumerGroupDefault != "demo" {
		t.Errorf("ConsumerGroupDefault = %q, want %q (cell ID)", spec.ConsumerGroupDefault, "demo")
	}
	if len(spec.Subscriptions) != 1 || spec.Subscriptions[0].ConsumerGroup != "" {
		t.Errorf("expected SubscriptionGenSpec.ConsumerGroup to remain empty (template falls back); got %+v", spec.Subscriptions)
	}
}

// TestBuildCellSpec_ListenerRefRejectsTypo verifies IMP-6: a typo in
// the bundle listener ref (e.g. "cell.PrimaryListenerXX") is rejected at
// BuildCellSpec time with a precise error rather than producing invalid Go.
func TestBuildCellSpec_ListenerRefRejectsTypo(t *testing.T) {
	t.Parallel()
	cases := []string{
		"cell.primaryListener",  // lowercase prefix
		"cell.Primary-Listener", // dash in identifier
		"primaryListener",       // missing cell. prefix
		"cell.",                 // empty identifier
	}
	for _, ref := range cases {
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
		p := fixtureProject(cell, nil, nil)
		bundle := markergen.WireBundle{
			Listeners: []markergen.ListenerSpec{{Ref: ref, Prefix: "/api/v1"}},
		}
		_, err := BuildCellSpec(p, "demo", bundle)
		if err == nil || !strings.Contains(err.Error(), "must match") {
			t.Errorf("BuildCellSpec with listener ref %q should error; got %v", ref, err)
		}
	}
}

// TestBuildCellSpec_TransportAlwaysAMQP verifies that the transport field in
// SubscriptionGenSpec is always "amqp" (bundle-derived subscribes do not carry
// a transport field; AMQP is the only supported transport in GoCell for now).
func TestBuildCellSpec_TransportAlwaysAMQP(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	contracts := []*metadata.ContractMeta{
		{ID: "event.foo.created.v1", Kind: "event"},
		{ID: "event.bar.updated.v1", Kind: "event"},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, contracts)
	bundle := markergen.WireBundle{
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "subs", Topic: "event.foo.created.v1", SliceField: "fooSvc", Handler: "HandleFooCreated", Group: "demo"},
			{Slice: "subs", Topic: "event.bar.updated.v1", SliceField: "barSvc", Handler: "HandleBarUpdated", Group: "demo"},
		},
	}

	spec, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Subscriptions) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(spec.Subscriptions))
	}
	for _, sub := range spec.Subscriptions {
		if sub.Transport != "amqp" {
			t.Errorf("transport = %q, want amqp", sub.Transport)
		}
	}
}
