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

func TestBuildCellSpec_HappyPath_OneListenerOneSubRoute(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "demo",
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	if sub.HandlerExpr != "c.barSvc.HandleBar" {
		t.Errorf("HandlerExpr = %q", sub.HandlerExpr)
	}
	if sub.SliceID != "subs" {
		t.Errorf("SliceID = %q", sub.SliceID)
	}
}

func TestBuildCellSpec_ConsumerGroupOverride(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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

// TestBuildCellSpec_ConsumerGroupEmptyFallsBackToCellID verifies IMP-3:
// when the subscribe marker omits group, BuildCellSpec leaves the
// SubscriptionGenSpec.ConsumerGroup empty and the template uses
// CellGenSpec.ConsumerGroupDefault (which is the cell ID). The empty-value
// path is the common case and must remain explicitly tested.
func TestBuildCellSpec_ConsumerGroupEmptyFallsBackToCellID(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
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

// TestBuildCellSpec_RouteMethodInvalidIdentRejected verifies K05-02: a
// non-empty Method that is not a valid exported Go identifier is rejected at
// BuildCellSpec time so the rendered `c.<HandlerField>.<Method>(s)` always
// compiles.
func TestBuildCellSpec_RouteMethodInvalidIdentRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"registerRoutes", // lowercase first letter — unexported
		"123Method",      // digit-leading
		"My-Method",      // dash in identifier
		" BadMethod",     // leading space
	}
	for _, method := range cases {
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
		slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
		bundle := markergen.WireBundle{
			Listeners: []markergen.ListenerSpec{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
			Routes: []markergen.RouteSpec{
				{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/x", HandlerField: "alphaH", Method: method},
			},
		}
		_, err := BuildCellSpec(p, "demo", bundle)
		if err == nil || !strings.Contains(err.Error(), "Method") {
			t.Errorf("BuildCellSpec with Method=%q should error with 'Method'; got %v", method, err)
		}
	}
}

// TestBuildCellSpec_RouteMethodEmptyAccepted verifies K05-02: an empty Method
// is valid (defaults to RegisterRoutes) and must not cause a validation error.
func TestBuildCellSpec_RouteMethodEmptyAccepted(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	bundle := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
		Routes: []markergen.RouteSpec{
			// Method intentionally empty — should default to RegisterRoutes without error.
			{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/x", HandlerField: "alphaH", Method: ""},
		},
	}
	_, err := BuildCellSpec(p, "demo", bundle)
	if err != nil {
		t.Fatalf("empty Method should be accepted; BuildCellSpec: %v", err)
	}
}

// TestBuildCellSpec_RouteHandlerFieldInvalidRejected verifies K05-02: a
// HandlerField that is not a valid Go identifier is rejected defensively.
func TestBuildCellSpec_RouteHandlerFieldInvalidRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"123handler",  // digit-leading
		"bad-handler", // dash
		"",            // empty
	}
	for _, field := range cases {
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
		slc := &metadata.SliceMeta{ID: "alpha", BelongsToCell: "demo", Dir: "alpha", File: "cells/demo/slices/alpha/slice.yaml"}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
		bundle := markergen.WireBundle{
			Listeners: []markergen.ListenerSpec{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
			Routes: []markergen.RouteSpec{
				{Slice: "alpha", Listener: "cell.PrimaryListener", SubPath: "/x", HandlerField: field},
			},
		}
		_, err := BuildCellSpec(p, "demo", bundle)
		if err == nil || !strings.Contains(err.Error(), "HandlerField") {
			t.Errorf("BuildCellSpec with HandlerField=%q should error with 'HandlerField'; got %v", field, err)
		}
	}
}

// TestBuildCellSpec_SubscribeHandlerInvalidRejected verifies K05-02: a
// Handler in a subscribe spec that is not an exported Go identifier is rejected
// so the rendered `c.<SliceField>.<Handler>` always compiles.
func TestBuildCellSpec_SubscribeHandlerInvalidRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"handleEvent", // lowercase first letter
		"123Handle",   // digit-leading
		"Handle-Bar",  // dash
		"",            // empty
	}
	for _, handler := range cases {
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
		slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})
		bundle := markergen.WireBundle{
			Subscribes: []markergen.SubscribeSpec{
				{Slice: "subs", Topic: "event.foo.bar.v1", SliceField: "barSvc", Handler: handler, Group: "demo"},
			},
		}
		_, err := BuildCellSpec(p, "demo", bundle)
		if err == nil || !strings.Contains(err.Error(), "Handler") {
			t.Errorf("BuildCellSpec with Handler=%q should error with 'Handler'; got %v", handler, err)
		}
	}
}

// TestBuildCellSpec_SubscribeSliceFieldInvalidRejected verifies K05-02: a
// SliceField that is not a valid Go identifier is rejected defensively.
func TestBuildCellSpec_SubscribeSliceFieldInvalidRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"123svc",  // digit-leading
		"bad-svc", // dash
		"",        // empty
	}
	for _, field := range cases {
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
		slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})
		bundle := markergen.WireBundle{
			Subscribes: []markergen.SubscribeSpec{
				{Slice: "subs", Topic: "event.foo.bar.v1", SliceField: field, Handler: "HandleBar", Group: "demo"},
			},
		}
		_, err := BuildCellSpec(p, "demo", bundle)
		if err == nil || !strings.Contains(err.Error(), "SliceField") {
			t.Errorf("BuildCellSpec with SliceField=%q should error with 'SliceField'; got %v", field, err)
		}
	}
}

// TestBuildCellSpec_SubscribeValidExportedIdentAccepted verifies K05-02:
// well-formed exported identifiers pass validation without error.
func TestBuildCellSpec_SubscribeValidExportedIdentAccepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		handler    string
		sliceField string
	}{
		{"HandleBar", "barSvc"},
		{"HandleOrderCreated", "orderSvc"},
		{"H", "s"},                    // minimal valid
		{"Handle_Event", "svc_field"}, // underscores valid
	}
	for _, tc := range cases {
		cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
		slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})
		bundle := markergen.WireBundle{
			Subscribes: []markergen.SubscribeSpec{
				{Slice: "subs", Topic: "event.foo.bar.v1", SliceField: tc.sliceField, Handler: tc.handler, Group: "demo"},
			},
		}
		_, err := BuildCellSpec(p, "demo", bundle)
		if err != nil {
			t.Errorf("BuildCellSpec with Handler=%q SliceField=%q should be accepted; got %v", tc.handler, tc.sliceField, err)
		}
	}
}

// TestBuildCellSpec_RenderedMetaLiteralPopulated locks CELLGEN-LITERAL-FUNNEL-02:
// CellGenSpec exposes a pre-rendered Go literal string (RenderedMetaLiteral), not
// the live *metadata.CellMeta pointer. The Hard property — cell.tmpl cannot
// hand-enumerate CellMeta fields because the struct is not reachable from the
// spec — depends on BuildCellSpec populating this field deterministically from
// renderCellMetaLiteral(cell). A regression that re-exposes *CellMeta or leaves
// the field blank breaks this test before reaching the template.
func TestBuildCellSpec_RenderedMetaLiteralPopulated(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:               "demo",
		Type:             "core",
		ConsistencyLevel: "L1",
		Dir:              "demo",
		File:             "cells/demo/cell.yaml",
		GoStructName:     metadata.MustNewGoIdentifier("Demo"),
	}
	p := fixtureProject(cell, nil, nil)

	spec, err := BuildCellSpec(p, "demo", markergen.WireBundle{})
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	got := spec.RenderedMetaLiteral
	if got == "" {
		t.Fatalf("RenderedMetaLiteral is empty; BuildCellSpec must pre-render the literal")
	}
	if !strings.HasPrefix(got, "&metadata.CellMeta{") {
		t.Errorf("RenderedMetaLiteral should start with '&metadata.CellMeta{', got: %q", got)
	}
	// Equivalence with renderCellMetaLiteral(cell) — locks BuildCellSpec to
	// the canonical renderer (not a hand-built string), which is the Hard
	// guarantee: any divergence here means BuildCellSpec stopped using the
	// reflect-driven funnel.
	if want := renderCellMetaLiteral(cell); got != want {
		t.Errorf("RenderedMetaLiteral diverges from renderCellMetaLiteral output\n got: %q\nwant: %q", got, want)
	}
}
