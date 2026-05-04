package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
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

func TestBuildCellSpec_HappyPath_OneListenerOneSubRoute(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "demo",
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: "Demo",
		Listeners:    []metadata.ListenerDeclMeta{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
	}
	slc := &metadata.SliceMeta{
		ID:            "alpha",
		BelongsToCell: "demo",
		Dir:           "alpha",
		File:          "cells/demo/slices/alpha/slice.yaml",
		RouteMounts: []metadata.RouteMountMeta{
			{Listener: "cell.PrimaryListener", SubPath: "/widgets", HandlerField: "alphaHandler"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildCellSpec(p, "demo")
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
	cell := &metadata.CellMeta{
		ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo",
		Listeners: []metadata.ListenerDeclMeta{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
	}
	slcA := &metadata.SliceMeta{
		ID: "alpha", BelongsToCell: "demo", Dir: "alpha",
		File:        "cells/demo/slices/alpha/slice.yaml",
		RouteMounts: []metadata.RouteMountMeta{{Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "createH"}},
	}
	slcB := &metadata.SliceMeta{
		ID: "beta", BelongsToCell: "demo", Dir: "beta",
		File:        "cells/demo/slices/beta/slice.yaml",
		RouteMounts: []metadata.RouteMountMeta{{Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "queryH"}},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slcA, slcB}, nil)

	spec, err := BuildCellSpec(p, "demo")
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
	cell := &metadata.CellMeta{
		ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo",
		Listeners: []metadata.ListenerDeclMeta{
			{Ref: "cell.PrimaryListener", Prefix: "/api/v1"},
			{Ref: "cell.InternalListener", Prefix: "/internal/v1"},
		},
	}
	slc := &metadata.SliceMeta{
		ID: "alpha", BelongsToCell: "demo", Dir: "alpha",
		File: "cells/demo/slices/alpha/slice.yaml",
		RouteMounts: []metadata.RouteMountMeta{
			{Listener: "cell.InternalListener", SubPath: "/admin", HandlerField: "adminH"},
			{Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "itemH"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildCellSpec(p, "demo")
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
	cell := &metadata.CellMeta{
		ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo",
		Listeners: []metadata.ListenerDeclMeta{{Ref: "cell.InternalListener", Prefix: "/internal/v1/admin"}},
	}
	slc := &metadata.SliceMeta{
		ID: "alpha", BelongsToCell: "demo", Dir: "alpha",
		File:        "cells/demo/slices/alpha/slice.yaml",
		RouteMounts: []metadata.RouteMountMeta{{Listener: "cell.InternalListener", SubPath: "", HandlerField: "adminH"}},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildCellSpec(p, "demo")
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
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs",
		File: "cells/demo/slices/subs/slice.yaml",
		Subscribes: []metadata.SubscribeDeclMeta{
			{Contract: "event.foo.bar.v1", SliceField: "barSvc", Handler: "HandleBar"},
		},
	}
	contract := &metadata.ContractMeta{ID: "event.foo.bar.v1", Kind: "event"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})

	spec, err := BuildCellSpec(p, "demo")
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
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs",
		File: "cells/demo/slices/subs/slice.yaml",
		Subscribes: []metadata.SubscribeDeclMeta{
			{Contract: "event.foo.bar.v1", SliceField: "barSvc", Handler: "HandleBar", ConsumerGroup: "demo-fanout"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})

	spec, err := BuildCellSpec(p, "demo")
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if spec.Subscriptions[0].ConsumerGroup != "demo-fanout" {
		t.Errorf("ConsumerGroup override lost: got %q", spec.Subscriptions[0].ConsumerGroup)
	}
}

func TestBuildCellSpec_NilProjectFails(t *testing.T) {
	t.Parallel()
	if _, err := BuildCellSpec(nil, "x"); err == nil {
		t.Fatal("expected error for nil project")
	}
}

func TestBuildCellSpec_UnknownCellFails(t *testing.T) {
	t.Parallel()
	p := fixtureProject(nil, nil, nil)
	_, err := BuildCellSpec(p, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestBuildCellSpec_MissingGoStructNameFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml"}
	p := fixtureProject(cell, nil, nil)
	_, err := BuildCellSpec(p, "demo")
	if err == nil || !strings.Contains(err.Error(), "goStructName") {
		t.Fatalf("expected goStructName error, got %v", err)
	}
}

func TestBuildCellSpec_DuplicateListenerFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo",
		Listeners: []metadata.ListenerDeclMeta{
			{Ref: "cell.PrimaryListener", Prefix: "/api/v1"},
			{Ref: "cell.PrimaryListener", Prefix: "/api/v2"},
		},
	}
	p := fixtureProject(cell, nil, nil)
	_, err := BuildCellSpec(p, "demo")
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("expected duplicate-listener error, got %v", err)
	}
}

func TestBuildCellSpec_RouteMountUndeclaredListenerFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{
		ID: "alpha", BelongsToCell: "demo", Dir: "alpha",
		File:        "cells/demo/slices/alpha/slice.yaml",
		RouteMounts: []metadata.RouteMountMeta{{Listener: "cell.PrimaryListener", SubPath: "/", HandlerField: "h"}},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	_, err := BuildCellSpec(p, "demo")
	if err == nil || !strings.Contains(err.Error(), "undeclared listener") {
		t.Fatalf("expected undeclared-listener error, got %v", err)
	}
}

func TestBuildCellSpec_UnknownContractFails(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs",
		File:       "cells/demo/slices/subs/slice.yaml",
		Subscribes: []metadata.SubscribeDeclMeta{{Contract: "ghost.event.v1", SliceField: "x", Handler: "Y"}},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	_, err := BuildCellSpec(p, "demo")
	if err == nil || !strings.Contains(err.Error(), "unknown contract") {
		t.Fatalf("expected unknown-contract error, got %v", err)
	}
}

func TestBuildCellSpec_NonEventContractRejected(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs",
		File:       "cells/demo/slices/subs/slice.yaml",
		Subscribes: []metadata.SubscribeDeclMeta{{Contract: "http.users.v1", SliceField: "x", Handler: "Y"}},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "http.users.v1", Kind: "http"}})
	_, err := BuildCellSpec(p, "demo")
	if err == nil || !strings.Contains(err.Error(), "non-event") {
		t.Fatalf("expected non-event-contract error, got %v", err)
	}
}

func TestBuildSliceSpec_NoSubscribesReturnsNil(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: "Demo"}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildSliceSpec(p, "demo", "alpha")
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
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs",
		File: "cells/demo/slices/subs/slice.yaml",
		Subscribes: []metadata.SubscribeDeclMeta{
			{Contract: "event.b.v1", SliceField: "svc", Handler: "HandleB"},
			{Contract: "event.a.v1", SliceField: "svc", Handler: "HandleA"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildSliceSpec(p, "demo", "subs")
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
	_, err := BuildSliceSpec(p, "x", "y")
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
		got := specVarName(tc.in)
		if got != tc.want {
			t.Errorf("specVarName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
