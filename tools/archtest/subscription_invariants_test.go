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
// AI-robust layering:
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
//
// RED-fixture self-checks (K#07 follow-up, gh #658):
//
// Each invariant's detection logic is extracted into a pure collector
// (collectSubscriptionFieldViolations / collectObservabilityIDViolations /
// collectSubscribeSignatureViolations) that returns violations instead of
// calling t.Errorf. The production test parses the real source and asserts
// the collector reports nothing; a sibling *_DetectorFixtures test feeds
// synthetic GREEN/RED source through the SAME collector and asserts it does
// (RED) / does not (GREEN) fire. This guards against the collector becoming
// structurally unable to fire — a vacuous pass that the production assertion,
// running only against already-valid source, cannot detect (e.g. an inverted
// allowlist check, an EachInSubtree that walks nothing, a dropped body-shape
// branch). Note: an allowlist-content typo is already caught by the production
// test directly (the real field lands in `unknown` AND the typo'd key lands in
// `missing`); the RED fixtures specifically cover detector-logic regressions.
//
// AI-robust rating: this is a Medium test-of-test reinforcement. The residual
// risk — a future refactor wiring a RED test to a *copy* of the detection
// logic rather than the shared collector — cannot be sealed at compile time
// (Go cannot force two test functions to share a function), the same permanent
// language ceiling as #851 / #893 holder-seals. The reachable maximum is a
// single shared collector per invariant (no duplicated logic), which this file
// satisfies. No gh issue tracks a Hard upgrade: the ceiling is identical to
// #851 / #893 (Go cannot express "two test functions must share a function"),
// so there is no deferred low-cost Hard path to track — this is a permanent
// ceiling, not a shortcut.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// parseSubscriptionFixtureSrc parses a synthetic Go source string into an AST
// for the RED/GREEN detector fixtures. It uses the same parser flags as the
// production paths (parser.SkipObjectResolution) so the collectors observe an
// identical AST shape; type resolution is intentionally absent — all three
// invariants are pure-AST checks.
func parseSubscriptionFixtureSrc(t *testing.T, src string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse synthetic fixture: %v", err)
	}
	return f, fset
}

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

// collectSubscriptionFieldViolations walks an AST for a struct named
// Subscription and reports field-set drift against subscriptionAllowedFields:
//   - unknown: declared fields not in the allowlist (and any embedded field).
//   - missing: allowlist fields not declared on the struct.
//
// found reports whether the Subscription struct was located at all. label
// prefixes the position in violation messages (real relative path in
// production; "fixture.go" in synthetic detector tests).
func collectSubscriptionFieldViolations(f *ast.File, fset *token.FileSet, label string) (found bool, unknown, missing []string) {
	seen := make(map[string]struct{})
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
				unknown = append(unknown, label+":"+strconv.Itoa(line)+": <embedded field>")
				continue
			}
			for _, name := range field.Names {
				seen[name.Name] = struct{}{}
				if _, ok := subscriptionAllowedFields[name.Name]; !ok {
					line := fset.Position(name.Pos()).Line
					unknown = append(unknown, label+":"+strconv.Itoa(line)+": "+name.Name)
				}
			}
		}
	})

	for k := range subscriptionAllowedFields {
		if _, ok := seen[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(unknown)
	sort.Strings(missing)
	return found, unknown, missing
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

	found, unknown, missing := collectSubscriptionFieldViolations(f, fset, "kernel/outbox/subscription.go")
	if !found {
		t.Fatalf("Subscription struct definition not found in kernel/outbox/subscription.go " +
			"— if the type was relocated, update this test's hardcoded path along with the move")
	}

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

// TestSubscriptionFieldsFrozen_DetectorFixtures is the RED-fixture self-check
// for SUBSCRIPTION-FIELDS-FROZEN-01. It exercises collectSubscriptionFieldViolations
// (the same collector the production test uses) against synthetic source so a
// regression that makes the collector unable to report drift fails here even
// while the production test keeps passing against valid real source.
func TestSubscriptionFieldsFrozen_DetectorFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		src         string
		wantFound   bool
		wantUnknown bool
		wantMissing bool
	}{
		{
			name: "green_exact_seven",
			src: `package fixture
type Subscription struct {
	Topic             string
	ConsumerGroup     string
	CellID            string
	SliceID           string
	ContractID        string
	ContractKind      string
	ContractTransport string
}`,
			wantFound: true,
		},
		{
			name: "red_extra_field",
			src: `package fixture
type Subscription struct {
	Topic             string
	ConsumerGroup     string
	CellID            string
	SliceID           string
	ContractID        string
	ContractKind      string
	ContractTransport string
	Extra             string
}`,
			wantFound:   true,
			wantUnknown: true,
		},
		{
			name: "red_missing_cellid",
			src: `package fixture
type Subscription struct {
	Topic             string
	ConsumerGroup     string
	SliceID           string
	ContractID        string
	ContractKind      string
	ContractTransport string
}`,
			wantFound:   true,
			wantMissing: true,
		},
		{
			name: "red_embedded_field",
			src: `package fixture
type Subscription struct {
	Topic             string
	ConsumerGroup     string
	CellID            string
	SliceID           string
	ContractID        string
	ContractKind      string
	ContractTransport string
	EmbeddedBase
}`,
			wantFound:   true,
			wantUnknown: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, fset := parseSubscriptionFixtureSrc(t, tc.src)
			found, unknown, missing := collectSubscriptionFieldViolations(f, fset, "fixture.go")
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if got := len(unknown) > 0; got != tc.wantUnknown {
				t.Errorf("unknown non-empty = %v (%v), want %v", got, unknown, tc.wantUnknown)
			}
			if got := len(missing) > 0; got != tc.wantMissing {
				t.Errorf("missing non-empty = %v (%v), want %v", got, missing, tc.wantMissing)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01
// ---------------------------------------------------------------------------

// collectObservabilityIDViolations walks an AST for a method named
// ObservabilityID on a (value or pointer) Subscription receiver and reports any
// deviation from the canonical single-statement `return s.CellID` body. found
// reports whether the method was located at all.
func collectObservabilityIDViolations(f *ast.File, fset *token.FileSet, label string) (found bool, violations []string) {
	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Name.Name != "ObservabilityID" {
			return
		}
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return
		}
		// Receiver type must be Subscription (value or pointer).
		recvField := fn.Recv.List[0]
		recvType := recvField.Type
		if star, ok := recvType.(*ast.StarExpr); ok {
			recvType = star.X
		}
		ident, ok := recvType.(*ast.Ident)
		if !ok || ident.Name != "Subscription" {
			return
		}
		var recvName string
		if len(recvField.Names) == 1 && recvField.Names[0] != nil {
			recvName = recvField.Names[0].Name
		}
		found = true
		if fn.Body == nil {
			violations = append(violations, label+": ObservabilityID has no body")
			return
		}
		// Body must contain exactly one statement: a return.
		if len(fn.Body.List) != 1 {
			violations = append(violations, fmt.Sprintf("%s:%d: ObservabilityID body must be a single "+
				"`return s.CellID` statement; got %d statements. Any if/switch fallback to ConsumerGroup "+
				"re-introduces a second source of truth — CellID is HARD-required by Registry.Subscribe "+
				"at codegen time. Delete the fallback.",
				label, fset.Position(fn.Body.Pos()).Line, len(fn.Body.List)))
			return
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			violations = append(violations, fmt.Sprintf("%s:%d: ObservabilityID body must be `return s.CellID`",
				label, fset.Position(fn.Body.Pos()).Line))
			return
		}
		sel, ok := ret.Results[0].(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "CellID" {
			violations = append(violations, fmt.Sprintf("%s:%d: ObservabilityID body must return s.CellID "+
				"directly (no fallback)", label, fset.Position(fn.Body.Pos()).Line))
			return
		}
		// The selector base must be the receiver itself (s.CellID), not some
		// other value's CellID field — `return other.CellID` would otherwise
		// satisfy the field-name check while reading a different source.
		if recvName != "" {
			base, ok := sel.X.(*ast.Ident)
			if !ok || base.Name != recvName {
				violations = append(violations, fmt.Sprintf("%s:%d: ObservabilityID body must return the "+
					"receiver's CellID (%s.CellID), not another value's CellID",
					label, fset.Position(fn.Body.Pos()).Line, recvName))
			}
		}
	})
	return found, violations
}

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

	found, violations := collectObservabilityIDViolations(f, fset, "kernel/outbox/subscription.go")
	if !found {
		t.Fatalf("ObservabilityID method on Subscription not found in kernel/outbox/subscription.go")
	}
	for _, v := range violations {
		t.Errorf("SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01: %s", v)
	}
}

// TestSubscriptionObservabilityNoFallback_DetectorFixtures is the RED-fixture
// self-check for SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01. It drives
// collectObservabilityIDViolations against synthetic source so a regression
// that stops detecting a ConsumerGroup fallback fails here.
func TestSubscriptionObservabilityNoFallback_DetectorFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		src            string
		wantFound      bool
		wantViolations bool
	}{
		{
			name: "green_return_cellid",
			src: `package fixture
type Subscription struct{}
func (s Subscription) ObservabilityID() string { return s.CellID }`,
			wantFound: true,
		},
		{
			name: "green_ptr_receiver",
			src: `package fixture
type Subscription struct{}
func (s *Subscription) ObservabilityID() string { return s.CellID }`,
			wantFound: true,
		},
		{
			name: "red_returns_other_dot_cellid",
			src: `package fixture
type Subscription struct{}
func (s Subscription) ObservabilityID() string { return other.CellID }`,
			wantFound:      true,
			wantViolations: true,
		},
		{
			name: "red_if_fallback",
			src: `package fixture
type Subscription struct{}
func (s Subscription) ObservabilityID() string {
	if s.CellID == "" {
		return s.ConsumerGroup
	}
	return s.CellID
}`,
			wantFound:      true,
			wantViolations: true,
		},
		{
			name: "red_returns_consumergroup",
			src: `package fixture
type Subscription struct{}
func (s Subscription) ObservabilityID() string { return s.ConsumerGroup }`,
			wantFound:      true,
			wantViolations: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, fset := parseSubscriptionFixtureSrc(t, tc.src)
			found, violations := collectObservabilityIDViolations(f, fset, "fixture.go")
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if got := len(violations) > 0; got != tc.wantViolations {
				t.Errorf("violations non-empty = %v (%v), want %v", got, violations, tc.wantViolations)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01
// ---------------------------------------------------------------------------

// collectSubscribeSignatureViolations verifies the FuncType describes
// `Subscribe(spec, handler, consumerGroup string, cellID string, opts ...SubscriptionOption) error`.
// It reports violations of:
//   - exactly 5 parameter groups (each group is one field; we forbid `a, b string` merging)
//   - 3rd parameter type is `string` (consumerGroup)
//   - 4th parameter type is `string` and named `cellID` (HARD positional)
//   - 5th parameter is variadic, element type `SubscriptionOption`
//
// label prefixes positions in violation messages. The merged-param, arity, and
// non-variadic branches return early because subsequent positional indexing
// would be meaningless once those structural preconditions fail.
func collectSubscribeSignatureViolations(fset *token.FileSet, ft *ast.FuncType, label string) []string {
	if ft.Params == nil {
		return []string{label + ": Subscribe method missing param list"}
	}
	var violations []string
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
		return append(violations, fmt.Sprintf("%s:%d: Subscribe must declare each parameter on its own "+
			"Field (no `a, b string` merging) — found %d names sharing type",
			label, fset.Position(p.Pos()).Line, len(p.Names)))
	}
	if len(flat) != 5 {
		return append(violations, fmt.Sprintf("%s:%d: Subscribe must have exactly 5 positional parameters "+
			"(spec, handler, consumerGroup, cellID, opts...); got %d",
			label, fset.Position(ft.Pos()).Line, len(flat)))
	}
	// 3rd param (consumerGroup) must be `string`.
	if !isIdent(flat[2].Type, "string") {
		violations = append(violations, fmt.Sprintf("%s:%d: Subscribe 3rd parameter must be `string` "+
			"(consumerGroup)", label, fset.Position(flat[2].Pos()).Line))
	}
	// 4th param (cellID) must be `string` and named cellID.
	if len(flat[3].Names) != 1 || flat[3].Names[0].Name != "cellID" {
		gotName := "<unnamed>"
		if len(flat[3].Names) == 1 {
			gotName = flat[3].Names[0].Name
		}
		violations = append(violations, fmt.Sprintf("%s:%d: Subscribe 4th parameter must be named `cellID`; "+
			"got %q. CellID is the AI-HARD positional contract — renaming or replacing it with a "+
			"SubscriptionOption demotes the contract from compile-time enforcement to opt-in.",
			label, fset.Position(flat[3].Pos()).Line, gotName))
	}
	if !isIdent(flat[3].Type, "string") {
		violations = append(violations, fmt.Sprintf("%s:%d: Subscribe 4th parameter must be `string` "+
			"(cellID); got non-string type", label, fset.Position(flat[3].Pos()).Line))
	}
	// 5th param must be variadic SubscriptionOption.
	ell, ok := flat[4].Type.(*ast.Ellipsis)
	if !ok {
		return append(violations, fmt.Sprintf("%s:%d: Subscribe 5th parameter must be variadic "+
			"(`...SubscriptionOption`)", label, fset.Position(flat[4].Pos()).Line))
	}
	if !isIdent(ell.Elt, "SubscriptionOption") {
		violations = append(violations, fmt.Sprintf("%s:%d: Subscribe variadic parameter element must be "+
			"`SubscriptionOption`", label, fset.Position(flat[4].Pos()).Line))
	}
	return violations
}

// TestRegistrySubscribeCellIDPositional enforces
// REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: the kernel/cell.Registrar.Subscribe
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
		if ts.Name == nil || ts.Name.Name != "Registrar" {
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
				if !ok {
					t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: Subscribe method type is not a func")
					return
				}
				for _, v := range collectSubscribeSignatureViolations(fset, ft, "kernel/cell/registry.go") {
					t.Errorf("REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01: %s", v)
				}
			}
		}
	})
	if !foundInterface {
		t.Fatalf("Registrar interface not found in kernel/cell/registry.go")
	}
	if !foundMethod {
		t.Fatalf("Subscribe method not found on Registrar interface in kernel/cell/registry.go")
	}
}

// subscribeFuncTypeFromSrc parses synthetic source and extracts the FuncType of
// the Subscribe method on a Registrar interface, for the detector fixtures.
func subscribeFuncTypeFromSrc(t *testing.T, src string) (*ast.FuncType, *token.FileSet) {
	t.Helper()
	f, fset := parseSubscriptionFixtureSrc(t, src)
	var ft *ast.FuncType
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "Registrar" {
			return
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok || iface.Methods == nil {
			return
		}
		for _, m := range iface.Methods.List {
			for _, name := range m.Names {
				if name.Name == "Subscribe" {
					if mt, ok := m.Type.(*ast.FuncType); ok {
						ft = mt
						return
					}
				}
			}
		}
	})
	if ft == nil {
		t.Fatalf("setup: Subscribe FuncType not found in synthetic fixture")
	}
	return ft, fset
}

// TestRegistrySubscribeCellIDPositional_DetectorFixtures is the RED-fixture
// self-check for REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01. It drives
// collectSubscribeSignatureViolations against synthetic interface declarations
// so a regression that stops detecting a demoted cellID positional fails here.
func TestRegistrySubscribeCellIDPositional_DetectorFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		src            string
		wantViolations bool
	}{
		{
			name: "green_canonical",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup string, cellID string, opts ...SubscriptionOption) error
}`,
		},
		{
			name: "red_merged_params",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup, cellID string, opts ...SubscriptionOption) error
}`,
			wantViolations: true,
		},
		{
			name: "red_3rd_not_string",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup ConsumerGroupID, cellID string, opts ...SubscriptionOption) error
}`,
			wantViolations: true,
		},
		{
			name: "red_wrong_4th_name",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup string, id string, opts ...SubscriptionOption) error
}`,
			wantViolations: true,
		},
		{
			name: "red_4th_not_string",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup string, cellID int, opts ...SubscriptionOption) error
}`,
			wantViolations: true,
		},
		{
			name: "red_5th_not_variadic",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup string, cellID string, opts SubscriptionOption) error
}`,
			wantViolations: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ft, fset := subscribeFuncTypeFromSrc(t, tc.src)
			violations := collectSubscribeSignatureViolations(fset, ft, "fixture.go")
			if got := len(violations) > 0; got != tc.wantViolations {
				t.Errorf("violations non-empty = %v (%v), want %v", got, violations, tc.wantViolations)
			}
		})
	}
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}
