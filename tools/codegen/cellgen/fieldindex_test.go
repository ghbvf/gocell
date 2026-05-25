package cellgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// TestIndexCellStructFields_HappyPath verifies that IndexCellStructFields
// parses a cell.go file and returns a map from slice package name to the
// struct field name, scanning *pkg.Type StarExpr → SelectorExpr fields.
func TestIndexCellStructFields_HappyPath(t *testing.T) {
	t.Parallel()
	src := `package democell

import (
	"github.com/example/democell/slices/orderprojection"
	"github.com/example/democell/slices/orderwrite"
)

type DemoCell struct {
	projectionSvc *orderprojection.Service
	writeSvc      *orderwrite.Handler
}
`
	path := writeTempCellGo(t, src)
	idx, err := IndexCellStructFields(path)
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	if idx["orderprojection"] != "projectionSvc" {
		t.Errorf("orderprojection → %q, want projectionSvc", idx["orderprojection"])
	}
	if idx["orderwrite"] != "writeSvc" {
		t.Errorf("orderwrite → %q, want writeSvc", idx["orderwrite"])
	}
}

// TestIndexCellStructFields_MissingFile returns an error when the file does not exist.
func TestIndexCellStructFields_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := IndexCellStructFields("/nonexistent/cell.go")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestIndexCellStructFields_NonPointerFieldsIgnored verifies that non-pointer
// fields (value types) are not indexed.
func TestIndexCellStructFields_NonPointerFieldsIgnored(t *testing.T) {
	t.Parallel()
	src := `package democell

type OrderProjectionService struct{}

type DemoCell struct {
	notAPointer OrderProjectionService
}
`
	path := writeTempCellGo(t, src)
	idx, err := IndexCellStructFields(path)
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	if _, ok := idx["OrderProjectionService"]; ok {
		t.Errorf("non-pointer field should not be indexed, got idx=%v", idx)
	}
}

// TestIndexCellStructFields_EmbeddedFieldsIgnored verifies that embedded
// (anonymous) fields are not indexed.
func TestIndexCellStructFields_EmbeddedFieldsIgnored(t *testing.T) {
	t.Parallel()
	src := `package democell

import "github.com/example/cell"

type DemoCell struct {
	cell.BaseCell
}
`
	path := writeTempCellGo(t, src)
	idx, err := IndexCellStructFields(path)
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	// Embedded fields have no name so should not appear in index.
	if len(idx) != 0 {
		t.Errorf("embedded field should not be indexed, got idx=%v", idx)
	}
}

// TestBuildCellSpec_SubscribesFromSliceYAML verifies that BuildCellSpec reads
// subscribe entries from p.Slices ContractUsages (not from bundle.Subscribes)
// when fieldIndex resolves the slice field name.
func TestBuildCellSpec_SubscribesFromSliceYAML(t *testing.T) {
	t.Parallel()
	cell, slc, contracts := buildSubscribeFixtures()
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, contracts)
	// fieldIndex maps slice ID → cell struct field name.
	fieldIndex := map[string]string{"subs": "subsSvc"}
	bundle := emptyBundleWithListener()

	spec, err := BuildCellSpec(p, "demo", bundle, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Subscriptions) != 1 {
		t.Fatalf("Subscriptions len = %d, want 1", len(spec.Subscriptions))
	}
	sub := spec.Subscriptions[0]
	if sub.HandlerExpr != "c.subsSvc.HandleFoo" {
		t.Errorf("HandlerExpr = %q, want c.subsSvc.HandleFoo", sub.HandlerExpr)
	}
	if sub.SliceID != "subs" {
		t.Errorf("SliceID = %q, want subs", sub.SliceID)
	}
	if sub.ConsumerGroup != "" {
		t.Errorf("ConsumerGroup should be empty (template falls back to cellID), got %q", sub.ConsumerGroup)
	}
}

// TestBuildCellSpec_SubscribeGroupExplicit verifies that a non-empty group
// in slice CU is passed through to SubscriptionGenSpec.
func TestBuildCellSpec_SubscribeGroupExplicit(t *testing.T) {
	t.Parallel()
	cell, slc, contracts := buildSubscribeFixturesWithGroup("demo-fanout")
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, contracts)
	fieldIndex := map[string]string{"subs": "subsSvc"}
	bundle := emptyBundleWithListener()

	spec, err := BuildCellSpec(p, "demo", bundle, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Subscriptions) != 1 {
		t.Fatalf("Subscriptions len = %d, want 1", len(spec.Subscriptions))
	}
	if spec.Subscriptions[0].ConsumerGroup != "demo-fanout" {
		t.Errorf("ConsumerGroup = %q, want demo-fanout", spec.Subscriptions[0].ConsumerGroup)
	}
}

// TestBuildCellSpec_SubscribeFieldNotInIndex verifies that a subscribe CU
// whose slice ID cannot be found in fieldIndex produces a fail-fast error
// (0 matches path).
func TestBuildCellSpec_SubscribeFieldNotInIndex(t *testing.T) {
	t.Parallel()
	cell, slc, contracts := buildSubscribeFixtures()
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, contracts)
	// Empty fieldIndex — slice field cannot be resolved.
	fieldIndex := map[string]string{}
	bundle := emptyBundleWithListener()

	_, err := BuildCellSpec(p, "demo", bundle, fieldIndex)
	if err == nil {
		t.Fatal("expected error when field not in index, got nil")
	}
	if !strings.Contains(err.Error(), "no cell.go struct field") {
		t.Errorf("error should mention 'no cell.go struct field', got %v", err)
	}
}

// TestBuildCellSpec_SubscribeHandlerMissingInCU verifies that a subscribe CU
// without a handler field produces a fail-fast error.
func TestBuildCellSpec_SubscribeHandlerMissingInCU(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "event.foo.v1", Role: "subscribe", Handler: ""}, // missing handler
		},
	}
	contract := &metadata.ContractMeta{ID: "event.foo.v1", Kind: "event"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := map[string]string{"subs": "subsSvc"}
	bundle := emptyBundleWithListener()

	_, err := BuildCellSpec(p, "demo", bundle, fieldIndex)
	if err == nil {
		t.Fatal("expected error when handler missing in CU, got nil")
	}
	if !strings.Contains(err.Error(), "Handler") {
		t.Errorf("error should mention 'Handler', got %v", err)
	}
}

// TestBuildCellSpec_BundleSubscribesNotConsumed verifies that bundle.Subscribes
// is no longer consumed by BuildCellSpec — subscriptions come only from slice CUs.
// A slice with no subscribe CUs must produce no subscriptions (single-source
// verification: subscriptions come exclusively from slice.yaml contractUsages).
func TestBuildCellSpec_BundleSubscribesNotConsumed(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	// Slice has no subscribe CUs — no subscriptions should be produced.
	slc := &metadata.SliceMeta{ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.bar.v1", Kind: "event"}})
	bundle := markergen.WireBundle{}

	spec, err := BuildCellSpec(p, "demo", bundle, nil)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Subscriptions) != 0 {
		t.Errorf("slice with no subscribe CUs should produce 0 subscriptions; got %d", len(spec.Subscriptions))
	}
}

// TestIndexCellStructFields_DuplicatePkgDeferred verifies that a package
// selector appearing on more than one field is marked ambiguous (sentinel "")
// rather than erroring at index time — harmless infra-package duplicates
// (auth, query, slog …) must not break indexing.
func TestIndexCellStructFields_DuplicatePkgDeferred(t *testing.T) {
	t.Parallel()
	src := `package democell

import "github.com/example/democell/slices/sessionlogout"

type DemoCell struct {
	logoutHandler       *sessionlogout.Handler
	rbacSessionConsumer *sessionlogout.Consumer
}
`
	path := writeTempCellGo(t, src)
	idx, err := IndexCellStructFields(path)
	if err != nil {
		t.Fatalf("IndexCellStructFields must not error on duplicate package: %v", err)
	}
	if idx["sessionlogout"] != ambiguousField {
		t.Errorf("duplicate package should map to ambiguousField sentinel, got %q", idx["sessionlogout"])
	}
}

// TestResolveSliceField_ExplicitFieldWins verifies the slice.yaml `field:`
// override is returned directly, bypassing package-convention resolution
// (and its ambiguity), so multi-field slices like sessionlogout resolve.
func TestResolveSliceField_ExplicitFieldWins(t *testing.T) {
	t.Parallel()
	idx := map[string]string{"sessionlogout": ambiguousField}
	got, err := resolveSliceField(idx, "rbacSessionConsumer", "accesscore", "sessionlogout")
	if err != nil {
		t.Fatalf("explicit field must resolve without error: %v", err)
	}
	if got != "rbacSessionConsumer" {
		t.Errorf("explicit field = %q, want rbacSessionConsumer", got)
	}
}

// TestResolveSliceField_AmbiguousWithoutField_Errors verifies an ambiguous
// package with no explicit field: produces an actionable disambiguation error.
func TestResolveSliceField_AmbiguousWithoutField_Errors(t *testing.T) {
	t.Parallel()
	idx := map[string]string{"sessionlogout": ambiguousField}
	_, err := resolveSliceField(idx, "", "accesscore", "sessionlogout")
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "disambiguate") {
		t.Errorf("error should suggest adding field: to disambiguate, got %v", err)
	}
}

// TestBuildCellSpec_SubscribeFieldOverride verifies a subscribe CU carrying
// field: resolves via the override even when the package index is ambiguous.
func TestBuildCellSpec_SubscribeFieldOverride(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "event.foo.v1", Role: "subscribe", Handler: "HandleFoo", Field: "consumerField"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.v1", Kind: "event"}})
	// Ambiguous index: convention resolution would fail, but field: wins.
	fieldIndex := map[string]string{"subs": ambiguousField}

	spec, err := BuildCellSpec(p, "demo", emptyBundleWithListener(), fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec with field override: %v", err)
	}
	if len(spec.Subscriptions) != 1 || spec.Subscriptions[0].HandlerExpr != "c.consumerField.HandleFoo" {
		t.Errorf("HandlerExpr = %+v, want c.consumerField.HandleFoo", spec.Subscriptions)
	}
}

// --- fixture helpers ---

func buildSubscribeFixtures() (*metadata.CellMeta, *metadata.SliceMeta, []*metadata.ContractMeta) {
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "event.foo.v1", Role: "subscribe", Handler: "HandleFoo"},
		},
	}
	contracts := []*metadata.ContractMeta{{ID: "event.foo.v1", Kind: "event"}}
	return cell, slc, contracts
}

func buildSubscribeFixturesWithGroup(group string) (*metadata.CellMeta, *metadata.SliceMeta, []*metadata.ContractMeta) {
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "event.foo.v1", Role: "subscribe", Handler: "HandleFoo", Group: group},
		},
	}
	contracts := []*metadata.ContractMeta{{ID: "event.foo.v1", Kind: "event"}}
	return cell, slc, contracts
}

func emptyBundleWithListener() markergen.WireBundle {
	return markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{{Ref: "cell.PrimaryListener", Prefix: "/api/v1"}},
	}
}

func writeTempCellGo(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cell.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write cell.go: %v", err)
	}
	return path
}
