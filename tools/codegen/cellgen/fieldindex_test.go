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
	idx, err := IndexCellStructFields(path, "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	if idx.byPkg["orderprojection"] != "projectionSvc" {
		t.Errorf("orderprojection → %q, want projectionSvc", idx.byPkg["orderprojection"])
	}
	if idx.byPkg["orderwrite"] != "writeSvc" {
		t.Errorf("orderwrite → %q, want writeSvc", idx.byPkg["orderwrite"])
	}
	// byField is the inverse, used for explicit field: validation.
	if idx.byField["projectionSvc"] != "orderprojection" {
		t.Errorf("byField[projectionSvc] = %q, want orderprojection", idx.byField["projectionSvc"])
	}
}

// TestIndexCellStructFields_MissingFile returns an error when the file does not exist.
func TestIndexCellStructFields_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := IndexCellStructFields("/nonexistent/cell.go", "DemoCell")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestIndexCellStructFields_HelperStructNotScanned verifies that pointer fields
// declared on a non-cell struct in the same cell.go (e.g. an options/config
// helper) do NOT enter the index — only the named cell struct is scanned. This
// is the F2 regression guard: a helper struct holding a *orderprojection.X
// field must not shadow or make-ambiguous the cell struct's own resolution.
func TestIndexCellStructFields_HelperStructNotScanned(t *testing.T) {
	t.Parallel()
	src := `package democell

import "github.com/example/democell/slices/orderprojection"

type DemoCell struct {
	projectionSvc *orderprojection.Service
}

// helperOptions is NOT the cell struct — its field must be ignored.
type helperOptions struct {
	shadow *orderprojection.Service
}
`
	path := writeTempCellGo(t, src)
	idx, err := IndexCellStructFields(path, "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	// The cell struct's field resolves cleanly — the helper struct's same-package
	// field did NOT flip orderprojection to ambiguous.
	if idx.byPkg["orderprojection"] != "projectionSvc" {
		t.Errorf("orderprojection → %q, want projectionSvc (helper struct must not pollute index)", idx.byPkg["orderprojection"])
	}
	if _, ok := idx.byField["shadow"]; ok {
		t.Errorf("helper struct field 'shadow' must not be indexed, got byField=%v", idx.byField)
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
	idx, err := IndexCellStructFields(path, "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	if _, ok := idx.byPkg["OrderProjectionService"]; ok {
		t.Errorf("non-pointer field should not be indexed, got byPkg=%v", idx.byPkg)
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
	idx, err := IndexCellStructFields(path, "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	// Embedded fields have no name so should not appear in index.
	if len(idx.byPkg) != 0 {
		t.Errorf("embedded field should not be indexed, got byPkg=%v", idx.byPkg)
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
	fieldIndex := idxOf(map[string]string{"subs": "subsSvc"})
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
	fieldIndex := idxOf(map[string]string{"subs": "subsSvc"})
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
	// Empty (but non-nil) fieldIndex — slice field cannot be resolved.
	fieldIndex := idxOf(map[string]string{})
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
	fieldIndex := idxOf(map[string]string{"subs": "subsSvc"})
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
	idx, err := IndexCellStructFields(path, "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields must not error on duplicate package: %v", err)
	}
	if idx.byPkg["sessionlogout"] != ambiguousField {
		t.Errorf("duplicate package should map to ambiguousField sentinel, got %q", idx.byPkg["sessionlogout"])
	}
	// byField retains BOTH field→pkg entries so an explicit field: can still
	// disambiguate which of the two same-package fields to bind.
	if idx.byField["logoutHandler"] != "sessionlogout" || idx.byField["rbacSessionConsumer"] != "sessionlogout" {
		t.Errorf("byField must retain both same-package fields, got %v", idx.byField)
	}
}

// indexFromSessionlogout builds the index for accesscore's sessionlogout
// scenario from real source: two *sessionlogout.T fields make the package
// ambiguous in byPkg while byField retains both.
func indexFromSessionlogout(t *testing.T) *CellFieldIndex {
	t.Helper()
	src := `package democell

import "github.com/example/democell/slices/sessionlogout"

type DemoCell struct {
	logoutHandler       *sessionlogout.Handler
	rbacSessionConsumer *sessionlogout.Consumer
}
`
	idx, err := IndexCellStructFields(writeTempCellGo(t, src), "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	return idx
}

// TestResolveSliceField_ExplicitFieldWins verifies the slice.yaml `field:`
// override resolves a multi-field slice like sessionlogout, where convention
// resolution is ambiguous. The explicit field is validated to belong to the
// subscribing slice's own package before it is accepted.
func TestResolveSliceField_ExplicitFieldWins(t *testing.T) {
	t.Parallel()
	idx := indexFromSessionlogout(t)
	got, err := idx.resolveSliceField("rbacSessionConsumer", "accesscore", "sessionlogout", roleSubscribe)
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
	idx := indexFromSessionlogout(t)
	_, err := idx.resolveSliceField("", "accesscore", "sessionlogout", roleSubscribe)
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "disambiguate") {
		t.Errorf("error should suggest adding field: to disambiguate, got %v", err)
	}
}

// TestResolveSliceField_ExplicitFieldWrongPackage_Errors is the F1 regression
// guard: an explicit field: that names an existing cell-struct field whose
// pointer type belongs to a DIFFERENT slice's package is rejected, instead of
// silently binding the subscription to the wrong slice (which could compile if
// that other type happens to expose a same-named handler method).
func TestResolveSliceField_ExplicitFieldWrongPackage_Errors(t *testing.T) {
	t.Parallel()
	src := `package democell

import (
	"github.com/example/democell/slices/subs"
	"github.com/example/democell/slices/other"
)

type DemoCell struct {
	subsSvc  *subs.Service
	otherSvc *other.Service
}
`
	idx, err := IndexCellStructFields(writeTempCellGo(t, src), "DemoCell")
	if err != nil {
		t.Fatalf("IndexCellStructFields: %v", err)
	}
	// subscribing slice is "subs" but field: points at otherSvc (*other.Service).
	_, err = idx.resolveSliceField("otherSvc", "demo", "subs", roleSubscribe)
	if err == nil {
		t.Fatal("expected error when field: points to a different slice's package, got nil")
	}
	if !strings.Contains(err.Error(), "different slice's package") {
		t.Errorf("error should mention 'different slice's package', got %v", err)
	}
}

// TestResolveSliceField_ExplicitFieldNotDeclared_Errors verifies that an
// explicit field: naming no cell-struct *pkg.T field at all is rejected with a
// descriptive error (rather than passing through to a c.<field>.<handler> that
// would fail to compile with an opaque message).
func TestResolveSliceField_ExplicitFieldNotDeclared_Errors(t *testing.T) {
	t.Parallel()
	idx := idxOf(map[string]string{"subs": "subsSvc"})
	_, err := idx.resolveSliceField("ghostField", "demo", "subs", roleSubscribe)
	if err == nil {
		t.Fatal("expected error when field: names no declared cell-struct field, got nil")
	}
	if !strings.Contains(err.Error(), "names no") {
		t.Errorf("error should mention field names no pointer field, got %v", err)
	}
}

// TestBuildCellSpec_SubscribeFieldOverride verifies a subscribe CU carrying
// field: resolves via the override even when the package index is ambiguous —
// and the override is validated to belong to the subscribing slice's package.
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
	// Two *subs.T fields → convention is ambiguous, but field: consumerField wins
	// and is validated to belong to package "subs".
	fieldIndex := &CellFieldIndex{
		byPkg:   map[string]string{"subs": ambiguousField},
		byField: map[string]string{"routeHandler": "subs", "consumerField": "subs"},
	}

	spec, err := BuildCellSpec(p, "demo", emptyBundleWithListener(), fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec with field override: %v", err)
	}
	if len(spec.Subscriptions) != 1 || spec.Subscriptions[0].HandlerExpr != "c.consumerField.HandleFoo" {
		t.Errorf("HandlerExpr = %+v, want c.consumerField.HandleFoo", spec.Subscriptions)
	}
}

// TestBuildCellSpec_SubscribeFieldUndeclared verifies that a subscribe CU
// carrying an explicit field: value that names no declared *pkg.T cell-struct
// field is rejected at BuildCellSpec time (rather than producing a
// c.<field>.<handler> expression that fails to compile with an opaque message).
func TestBuildCellSpec_SubscribeFieldUndeclared(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml", GoStructName: metadata.MustNewGoIdentifier("Demo")}
	slc := &metadata.SliceMeta{
		ID: "subs", BelongsToCell: "demo", Dir: "subs", File: "cells/demo/slices/subs/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "event.foo.v1", Role: "subscribe", Handler: "HandleFoo", Field: "ghostField"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{{ID: "event.foo.v1", Kind: "event"}})
	fieldIndex := idxOf(map[string]string{"subs": "subsSvc"})
	bundle := emptyBundleWithListener()

	_, err := BuildCellSpec(p, "demo", bundle, fieldIndex)
	if err == nil {
		t.Fatal("expected error for undeclared field, got nil")
	}
	if !strings.Contains(err.Error(), "names no") {
		t.Errorf("error should mention field names no pointer field, got: %v", err)
	}
}

// TestBuildCellSpec_NilFieldIndexWithSubscribeCU verifies that passing nil
// fieldIndex together with a subscribe CU (rather than an empty map) produces
// an error mentioning "fieldIndex is nil". Previously no test covered this
// specific code path in resolveSliceField.
func TestBuildCellSpec_NilFieldIndexWithSubscribeCU(t *testing.T) {
	t.Parallel()
	cell, slc, contracts := buildSubscribeFixtures()
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, contracts)
	bundle := emptyBundleWithListener()

	_, err := BuildCellSpec(p, "demo", bundle, nil) // nil fieldIndex, not empty map
	if err == nil {
		t.Fatal("expected error when fieldIndex is nil and subscribe CU present, got nil")
	}
	if !strings.Contains(err.Error(), "fieldIndex is nil") {
		t.Errorf("error should mention 'fieldIndex is nil', got: %v", err)
	}
}

// --- fixture helpers ---

// idxOf builds a *CellFieldIndex from a pkg→field map, deriving the inverse
// byField (skipping ambiguousField entries, which carry no single field name).
// For scenarios that need a specific byField shape (ambiguous package with an
// explicit disambiguating field), construct *CellFieldIndex directly or build
// from real source via IndexCellStructFields.
func idxOf(byPkg map[string]string) *CellFieldIndex {
	bf := make(map[string]string, len(byPkg))
	for pkg, field := range byPkg {
		if field != ambiguousField {
			bf[field] = pkg
		}
	}
	return &CellFieldIndex{byPkg: byPkg, byField: bf}
}

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
