// invariants:
//   - INVARIANT: SUBSCRIPTION-FIELDS-FROZEN-01
//   - INVARIANT: SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01
//   - INVARIANT: REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01
//
// Package archtest — subscription identity invariants.
//
// Background (K#07 PR-V1-EVENTROUTER-SUBSCRIPTION-FIELDS):
//
// CellID + ConsumerGroup are two distinct semantic axes on outbox.Subscription:
//
//   - CellID is observability owner (metrics/log/trace owner label). Single
//     source of truth = cell metadata, injected at codegen time into the
//     reg.Subscribe call site. No runtime fallback, no bootstrap drain
//     workaround.
//   - ConsumerGroup is broker partition key + idempotency namespace.
//
// AI-rebust layering:
//
//   - HARD (compile-time): Registry.Subscribe(spec, handler, consumerGroup,
//     cellID, opts...) requires cellID as a positional parameter. Omission
//     is a compile failure at the call site, and so is misuse of cellID as
//     a SubscriptionOption.
//   - MEDIUM (this file): three AST/typeseval invariants pin the
//     Subscription field set, the absence of an ObservabilityID fallback,
//     and the Subscribe method signature shape. They are the safety net
//     for an AI session that tries to re-introduce the historical
//     "CellID==\"\" → fallback to ConsumerGroup" behavior or to add a
//     WithSubscriptionCellID option that would relax the Hard contract.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ---------------------------------------------------------------------------
// SUBSCRIPTION-FIELDS-FROZEN-01
// ---------------------------------------------------------------------------

// subscriptionAllowedFields is the verbatim field set of kernel/outbox.Subscription.
// Adding an eighth field requires extending this allowlist deliberately, which
// is the moment to (a) decide whether the field belongs on the cross-middleware
// Subscription identity or on SubscriptionRequest/handlerConfig (eventrouter
// internal), (b) re-read ADR 202605111000-adr-subscription-cellid-mandatory.md
// (W2 of K#07), and (c) confirm whether codegen/cellgen must inject the value.
var subscriptionAllowedFields = map[string]struct{}{
	"Topic":             {},
	"ConsumerGroup":     {},
	"CellID":            {},
	"SliceID":           {},
	"ContractID":        {},
	"ContractKind":      {},
	"ContractTransport": {},
}

// TestSubscriptionFieldsFrozen enforces SUBSCRIPTION-FIELDS-FROZEN-01:
// kernel/outbox.Subscription must declare exactly the seven fields listed in
// subscriptionAllowedFields. Drift in this field set silently changes what
// every cell handler can/must produce on a Subscription literal AND what
// codegen (contractgen + cellgen) must inject; freezing the set keeps the
// observability/broker-routing axes intentional.
//
// Cannot funnel: Subscription is a kernel-owned type whose literal
// construction is required by codegen-produced subscription_gen.go and by
// conformance harness helpers. Making fields unexported to force factory-only
// access would break that intra-package construction.
func TestSubscriptionFieldsFrozen(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "outbox", "subscription.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var (
		found   bool
		seen    = make(map[string]struct{})
		unknown []string
	)
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "Subscription" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		found = true
		for _, field := range st.Fields.List {
			if len(field.Names) == 0 {
				line := fset.Position(field.Type.Pos()).Line
				unknown = append(unknown, "kernel/outbox/subscription.go:"+strconv.Itoa(line)+": <embedded field>")
				continue
			}
			for _, name := range field.Names {
				seen[name.Name] = struct{}{}
				if _, ok := subscriptionAllowedFields[name.Name]; !ok {
					line := fset.Position(name.Pos()).Line
					unknown = append(unknown, "kernel/outbox/subscription.go:"+strconv.Itoa(line)+": "+name.Name)
				}
			}
		}
	})

	if !found {
		t.Fatalf("Subscription struct definition not found in kernel/outbox/subscription.go " +
			"— if the type was relocated, update this test's hardcoded path along with the move")
	}

	var missing []string
	for k := range subscriptionAllowedFields {
		if _, ok := seen[k]; !ok {
			missing = append(missing, k)
		}
	}

	sort.Strings(unknown)
	sort.Strings(missing)
	for _, u := range unknown {
		t.Errorf("SUBSCRIPTION-FIELDS-FROZEN-01: %s — field not in allowlist; "+
			"to add a field, update subscriptionAllowedFields and review "+
			"ADR 202605111000-adr-subscription-cellid-mandatory.md plus codegen "+
			"(contractgen subscription.tmpl + cellgen cell.tmpl) so the new field "+
			"is injected on every reg.Subscribe call site", u)
	}
	for _, m := range missing {
		t.Errorf("SUBSCRIPTION-FIELDS-FROZEN-01: required field %s missing from "+
			"kernel/outbox.Subscription — removing a field changes the cross-middleware "+
			"identity contract; review ADR 202605111000 and the codegen templates "+
			"before relaxing the allowlist", m)
	}
}

// ---------------------------------------------------------------------------
// SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01
// ---------------------------------------------------------------------------

// TestSubscriptionObservabilityNoFallback enforces
// SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01: the body of
// kernel/outbox.Subscription.ObservabilityID must be a single
// `return s.CellID` statement, with no if/switch fallback to ConsumerGroup.
//
// Rationale: CellID is set unconditionally at codegen time (HARD positional
// parameter on Registry.Subscribe). Any runtime fallback creates a second
// source of truth — metrics/log labels would silently substitute
// ConsumerGroup when codegen breaks, masking the real defect. K#07 deletes
// the fallback; this archtest prevents re-introduction.
func TestSubscriptionObservabilityNoFallback(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "outbox", "subscription.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var found bool
	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Name.Name != "ObservabilityID" {
			return
		}
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return
		}
		// Receiver type must be Subscription (value or pointer).
		recvType := fn.Recv.List[0].Type
		if star, ok := recvType.(*ast.StarExpr); ok {
			recvType = star.X
		}
		ident, ok := recvType.(*ast.Ident)
		if !ok || ident.Name != "Subscription" {
			return
		}
		found = true
		if fn.Body == nil {
			t.Errorf("SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01: ObservabilityID has no body")
			return
		}
		// Body must contain exactly one statement: a return.
		if len(fn.Body.List) != 1 {
			t.Errorf("SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01: ObservabilityID body must be a single "+
				"`return s.CellID` statement; got %d statements at kernel/outbox/subscription.go:%d. "+
				"Any if/switch fallback to ConsumerGroup re-introduces a second source of truth — "+
				"CellID is HARD-required by Registry.Subscribe at codegen time. Delete the fallback.",
				len(fn.Body.List), fset.Position(fn.Body.Pos()).Line)
			return
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			t.Errorf("SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01: ObservabilityID body must be "+
				"`return s.CellID` at kernel/outbox/subscription.go:%d",
				fset.Position(fn.Body.Pos()).Line)
			return
		}
		sel, ok := ret.Results[0].(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "CellID" {
			t.Errorf("SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01: ObservabilityID body must return "+
				"s.CellID directly (no fallback); got at kernel/outbox/subscription.go:%d",
				fset.Position(fn.Body.Pos()).Line)
		}
	})
	if !found {
		t.Fatalf("ObservabilityID method on Subscription not found in kernel/outbox/subscription.go")
	}
}

// ---------------------------------------------------------------------------
// REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01
// ---------------------------------------------------------------------------

// TestRegistrySubscribeCellIDPositional enforces
// REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: the kernel/cell.Registry.Subscribe
// method must declare cellID as the 4th positional string parameter (after
// spec, handler, consumerGroup), and SubscriptionOption may only appear as
// the final variadic parameter.
//
// This is the AI-HARD compile-time gate's safety net: changing cellID from
// positional `cellID string` to an option (`WithSubscriptionCellID(...)`)
// would silently relax the contract from HARD to SOFT — callers could omit
// it and the codegen template would produce no-cellID call sites. By
// pinning the signature shape via AST, we prevent that demotion across
// AI sessions.
//
// Method signature reference (post-K#07):
//
//	Subscribe(spec contractspec.ContractSpec,
//	          handler outbox.EntryHandler,
//	          consumerGroup string,
//	          cellID string,
//	          opts ...SubscriptionOption) error
func TestRegistrySubscribeCellIDPositional(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "cell", "registry.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var (
		foundInterface bool
		foundMethod    bool
	)
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "Registry" {
			return
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok || iface.Methods == nil {
			return
		}
		foundInterface = true
		for _, m := range iface.Methods.List {
			if len(m.Names) == 0 {
				continue
			}
			for _, name := range m.Names {
				if name.Name != "Subscribe" {
					continue
				}
				foundMethod = true
				ft, ok := m.Type.(*ast.FuncType)
				if !ok || ft.Params == nil {
					t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe method missing param list")
					return
				}
				assertSubscribeSignature(t, fset, ft)
			}
		}
	})
	if !foundInterface {
		t.Fatalf("Registry interface not found in kernel/cell/registry.go")
	}
	if !foundMethod {
		t.Fatalf("Subscribe method not found on Registry interface in kernel/cell/registry.go")
	}
}

// assertSubscribeSignature verifies the FuncType describes
// `Subscribe(spec, handler, consumerGroup string, cellID string, opts ...SubscriptionOption) error`.
// It enforces:
//   - exactly 5 parameter groups (each group is one field; we forbid `a, b string` merging)
//   - 3rd parameter type is `string` (consumerGroup)
//   - 4th parameter type is `string` and named `cellID` (HARD positional)
//   - 5th parameter is variadic, element type `SubscriptionOption`
func assertSubscribeSignature(t *testing.T, fset *token.FileSet, ft *ast.FuncType) {
	t.Helper()
	params := ft.Params.List
	// Each param must be its own Field (one Name per Field) — no `(a, b string)` style merging.
	flat := make([]*ast.Field, 0, len(params))
	for _, p := range params {
		if len(p.Names) <= 1 {
			flat = append(flat, p)
			continue
		}
		// Reject merged params: each named position must be its own Field so
		// we can pin "4th positional param is cellID string" by index.
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe must declare each "+
			"parameter on its own Field (no `a, b string` merging) at "+
			"kernel/cell/registry.go:%d — found %d names sharing type",
			fset.Position(p.Pos()).Line, len(p.Names))
		return
	}
	if len(flat) != 5 {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe must have exactly 5 positional "+
			"parameters (spec, handler, consumerGroup, cellID, opts...); got %d at "+
			"kernel/cell/registry.go:%d", len(flat), fset.Position(ft.Pos()).Line)
		return
	}
	// 3rd param (consumerGroup) must be `string`.
	if !isIdent(flat[2].Type, "string") {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe 3rd parameter must be `string` "+
			"(consumerGroup) at kernel/cell/registry.go:%d",
			fset.Position(flat[2].Pos()).Line)
	}
	// 4th param (cellID) must be `string` and named cellID.
	if len(flat[3].Names) != 1 || flat[3].Names[0].Name != "cellID" {
		gotName := "<unnamed>"
		if len(flat[3].Names) == 1 {
			gotName = flat[3].Names[0].Name
		}
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe 4th parameter must be named "+
			"`cellID`; got %q at kernel/cell/registry.go:%d. CellID is the AI-HARD positional "+
			"contract — renaming or replacing it with a SubscriptionOption demotes the contract "+
			"from compile-time enforcement to opt-in.",
			gotName, fset.Position(flat[3].Pos()).Line)
	}
	if !isIdent(flat[3].Type, "string") {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe 4th parameter must be `string` "+
			"(cellID); got non-string type at kernel/cell/registry.go:%d",
			fset.Position(flat[3].Pos()).Line)
	}
	// 5th param must be variadic SubscriptionOption.
	ell, ok := flat[4].Type.(*ast.Ellipsis)
	if !ok {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe 5th parameter must be "+
			"variadic (`...SubscriptionOption`) at kernel/cell/registry.go:%d",
			fset.Position(flat[4].Pos()).Line)
		return
	}
	if !isIdent(ell.Elt, "SubscriptionOption") {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe variadic parameter element "+
			"must be `SubscriptionOption` at kernel/cell/registry.go:%d",
			fset.Position(flat[4].Pos()).Line)
	}
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}
