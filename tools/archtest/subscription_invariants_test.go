//go:build archtest

// invariants:
//   - INVARIANT: SUBSCRIPTION-FIELDS-FROZEN-01
//   - INVARIANT: SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01
//   - INVARIANT: REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01
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
//   - HARD (compile-time): cellID is mandatory in BOTH registration forms.
//     (a) Positional Registry.Subscribe(spec, handler, consumerGroup, cellID,
//     opts...) — codegen's form; omitting cellID is a compile failure, and so
//     is misuse of cellID as a SubscriptionOption.
//     (b) Fluent Registry.Subscription(spec).CellID(id)… — the hand-written
//     form; the terminal Register lives only on *subscriptionBuilder, which is
//     reachable only by calling CellID on the *subscriptionDraft, so skipping
//     CellID leaves no path to Register (compile failure).
//   - MEDIUM (this file): AST invariants pin the Subscription field set, the
//     absence of an ObservabilityID fallback, the positional Subscribe signature
//     shape, AND the two-type builder method-set / return-type shape that keeps
//     the missing-CellID path unreachable. They are the safety net for an AI
//     session that tries to re-introduce the historical "CellID==\"\" → fallback
//     to ConsumerGroup" behavior, add a WithSubscriptionCellID option, or
//     collapse the builder to a single type with an optional CellID.
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
// Adding a field requires extending this allowlist deliberately, which is the
// moment to (a) decide whether the field belongs on the cross-middleware
// Subscription identity or on SubscriptionRequest/handlerConfig (eventrouter
// internal), (b) re-read ADR 202605111000-adr-subscription-cellid-mandatory.md
// (W2 of K#07), and (c) confirm whether codegen/cellgen must inject the value.
//
// BrokerDelaySchedule (#1458) belongs on the cross-middleware Subscription
// because the Subscriber (the consume-side adapter) is what honors the
// per-attempt delay. Unlike CellID/SliceID it is NOT codegen-injected on
// reg.Subscribe: it is wiring-injected by the webhook-dispatch bootstrap drain
// (runtime/bootstrap/phases_events.go) via cell.WithSubscriptionBrokerDelaySchedule,
// so contractgen/cellgen templates intentionally do not set it (the zero value —
// nil — is correct for every codegen-registered cell subscription).
var subscriptionAllowedFields = map[string]struct{}{
	"Topic":               {},
	"ConsumerGroup":       {},
	"CellID":              {},
	"SliceID":             {},
	"ContractID":          {},
	"ContractKind":        {},
	"ContractTransport":   {},
	"BrokerDelaySchedule": {},
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
// kernel/outbox.Subscription must declare exactly the eight fields listed in
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
	path := filepath.Join(root, "framework", "kernel", "outbox", "subscription.go")
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
			name: "green_exact_allowlist",
			src: `package fixture
type Subscription struct {
	Topic               string
	ConsumerGroup       string
	CellID              string
	SliceID             string
	ContractID          string
	ContractKind        string
	ContractTransport   string
	BrokerDelaySchedule []time.Duration
}`,
			wantFound: true,
		},
		{
			name: "red_extra_field",
			src: `package fixture
type Subscription struct {
	Topic               string
	ConsumerGroup       string
	CellID              string
	SliceID             string
	ContractID          string
	ContractKind        string
	ContractTransport   string
	BrokerDelaySchedule []time.Duration
	Extra               string
}`,
			wantFound:   true,
			wantUnknown: true,
		},
		{
			name: "red_missing_cellid",
			src: `package fixture
type Subscription struct {
	Topic               string
	ConsumerGroup       string
	SliceID             string
	ContractID          string
	ContractKind        string
	ContractTransport   string
	BrokerDelaySchedule []time.Duration
}`,
			wantFound:   true,
			wantMissing: true,
		},
		{
			name: "red_embedded_field",
			src: `package fixture
type Subscription struct {
	Topic               string
	ConsumerGroup       string
	CellID              string
	SliceID             string
	ContractID          string
	ContractKind        string
	ContractTransport   string
	BrokerDelaySchedule []time.Duration
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
//
// The receiver must be named: an anonymous receiver cannot reference s.CellID,
// so it can never express the canonical body, and it would silently disable the
// selector-base check (there is no receiver name to anchor `s.CellID` against),
// letting `return other.CellID` read a foreign value while still passing the
// field-name check. An anonymous receiver is therefore a violation outright.
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
		if recvName == "" {
			violations = append(violations, fmt.Sprintf("%s:%d: ObservabilityID must use a named receiver "+
				"(`func (s Subscription) ObservabilityID()`); an anonymous receiver cannot reference s.CellID "+
				"and disables the selector-base check, letting `return other.CellID` read a foreign value",
				label, fset.Position(recvField.Pos()).Line))
			return
		}
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
		// satisfy the field-name check while reading a different source. recvName
		// is guaranteed non-empty here (anonymous receivers returned above), so
		// this check is unconditional.
		base, ok := sel.X.(*ast.Ident)
		if !ok || base.Name != recvName {
			violations = append(violations, fmt.Sprintf("%s:%d: ObservabilityID body must return the "+
				"receiver's CellID (%s.CellID), not another value's CellID",
				label, fset.Position(fn.Body.Pos()).Line, recvName))
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
	path := filepath.Join(root, "framework", "kernel", "outbox", "subscription.go")
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
			// Anonymous receiver: there is no receiver name to anchor the
			// selector base against, so `other.CellID` satisfies the field-name
			// check while reading a foreign value. An anonymous receiver also
			// cannot reference s.CellID at all, so it can never express the
			// canonical body — it must be a violation outright.
			name: "red_anonymous_receiver_other_cellid",
			src: `package fixture
type Subscription struct{}
func (Subscription) ObservabilityID() string { return other.CellID }`,
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
// REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01 (prong 1: positional signature)
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

// TestRegistrySubscribeCellIDPositional enforces prong 1 of
// REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01: the kernel/cell.Registrar.Subscribe
// method must declare cellID as the 4th positional string parameter (after
// spec, handler, consumerGroup), and SubscriptionOption may only appear as
// the final variadic parameter. It also pins that the fluent entry point
// Registrar.Subscription returns *subscriptionDraft — the draft type whose only
// method (CellID) is the sole door to the terminal Register (see prong 2,
// TestRegistrySubscribeCellIDMandatory_BuilderShape).
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
	path := filepath.Join(root, "framework", "kernel", "cell", "registry.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var (
		foundInterface    bool
		foundMethod       bool
		foundSubscription bool
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
			ft, ok := m.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			for _, name := range m.Names {
				switch name.Name {
				case "Subscribe":
					foundMethod = true
					for _, v := range collectSubscribeSignatureViolations(fset, ft, "kernel/cell/registry.go") {
						t.Errorf("REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01: %s", v)
					}
				case "Subscription":
					foundSubscription = true
					// The fluent entry point must return *subscriptionDraft — the
					// draft whose only method (CellID) gates the terminal Register.
					// Returning the builder (or anything else) directly would let a
					// caller reach Register without CellID.
					if ft.Results == nil || len(ft.Results.List) != 1 ||
						!isPtrIdent(ft.Results.List[0].Type, "subscriptionDraft") {
						t.Errorf("REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01: Registrar.Subscription must return " +
							"*subscriptionDraft (the CellID-gated draft); a different return type would let a " +
							"caller reach Register without naming the cell")
					}
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
	if !foundSubscription {
		t.Fatalf("Subscription method not found on Registrar interface in kernel/cell/registry.go")
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
// self-check for REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01. It drives
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
		{
			// Variadic, but the element type is not SubscriptionOption. Guards
			// the ell.Elt type check at the bottom of the collector — without
			// this row, an inverted/dropped element-type branch would pass
			// vacuously (every other red row returns early before reaching it).
			name: "red_5th_wrong_variadic_element",
			src: `package fixture
type Registrar interface {
	Subscribe(spec ContractSpec, handler EntryHandler, consumerGroup string, cellID string, opts ...OtherOption) error
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

func isPtrIdent(expr ast.Expr, name string) bool {
	star, ok := expr.(*ast.StarExpr)
	return ok && isIdent(star.X, name)
}

// ---------------------------------------------------------------------------
// REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01 (prong 2: builder type-state shape)
// ---------------------------------------------------------------------------

const (
	subDraftType   = "subscriptionDraft"
	subBuilderType = "subscriptionBuilder"
)

// receiverBaseName returns the (pointer-stripped) named receiver type of a
// method declaration, e.g. "subscriptionDraft" for `func (d *subscriptionDraft)`.
func receiverBaseName(fd *ast.FuncDecl) (string, bool) {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return "", false
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	id, ok := t.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// firstResultType returns the first result type expression of a method, or nil
// when the method declares no results.
func firstResultType(fd *ast.FuncDecl) ast.Expr {
	if fd.Type.Results == nil || len(fd.Type.Results.List) == 0 {
		return nil
	}
	return fd.Type.Results.List[0].Type
}

// diffMethodSet reports a violation per missing or unexpected method, freezing
// receiver typeName's method set to exactly want.
func diffMethodSet(label, typeName string, got map[string]ast.Expr, want []string) []string {
	wantSet := make(map[string]struct{}, len(want))
	for _, w := range want {
		wantSet[w] = struct{}{}
	}
	var v []string
	for name := range got {
		if _, ok := wantSet[name]; !ok {
			v = append(v, fmt.Sprintf("%s: %s has unexpected method %q (method set frozen to %v)",
				label, typeName, name, want))
		}
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			v = append(v, fmt.Sprintf("%s: %s missing required method %q (method set frozen to %v)",
				label, typeName, w, want))
		}
	}
	return v
}

// collectSubscriptionBuilderShapeViolations freezes the two-type type-state
// builder so the missing-CellID path stays unreachable at compile time:
//
//   - subscriptionDraft exposes EXACTLY {CellID} (no Register), and CellID
//     returns *subscriptionBuilder — the sole door to the terminal Register.
//   - subscriptionBuilder exposes EXACTLY {ConsumerGroup, Handler, SliceID,
//     Register}, and Register returns error.
//
// Collapsing the two types into one (optional-CellID regression), giving the
// draft a Register, or changing CellID's return type would each surface here.
func collectSubscriptionBuilderShapeViolations(_ *token.FileSet, f *ast.File, label string) []string {
	draft := map[string]ast.Expr{}
	builder := map[string]ast.Expr{}
	scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		name, ok := receiverBaseName(fd)
		if !ok {
			return
		}
		switch name {
		case subDraftType:
			draft[fd.Name.Name] = firstResultType(fd)
		case subBuilderType:
			builder[fd.Name.Name] = firstResultType(fd)
		}
	})

	var v []string
	v = append(v, diffMethodSet(label, subDraftType, draft, []string{"CellID"})...)
	v = append(v, diffMethodSet(label, subBuilderType, builder,
		[]string{"ConsumerGroup", "Handler", "SliceID", "Register"})...)

	// CellID must return *subscriptionBuilder (the only path to Register).
	if rt, ok := draft["CellID"]; ok && !isPtrIdent(rt, subBuilderType) {
		v = append(v, fmt.Sprintf("%s: %s.CellID must return *%s (the sole path to the terminal Register); "+
			"a different return type would open or sever the type-state gate", label, subDraftType, subBuilderType))
	}
	// Register must return error.
	if rt, ok := builder["Register"]; ok && !isIdent(rt, "error") {
		v = append(v, fmt.Sprintf("%s: %s.Register must return error", label, subBuilderType))
	}
	return v
}

// TestRegistrySubscribeCellIDMandatory_BuilderShape enforces prong 2 of
// REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01: the hand-written fluent builder keeps
// CellID a compile-time red line by construction. subscriptionDraft has exactly
// one method (CellID → *subscriptionBuilder) and no Register; subscriptionBuilder
// owns the terminal Register. Freezing both method sets prevents an AI session
// from collapsing the builder to a single type with an optional CellID, which
// would demote the contract from compile-time to runtime fail-fast.
//
// Detector blind spots (vs scanner.EachInSubtree[ast.FuncDecl] + AST receiver
// resolution): the collector keys on the receiver's pointer-stripped *ast.Ident
// name, so a value receiver `func (b subscriptionBuilder)` is also counted
// (covered — receiverBaseName strips no Star but matches the Ident); an embedded
// method promoted from another type would NOT be seen (Go AST has no embedded
// method decl in this file — the builder embeds nothing, asserted by the
// FIELDS-FROZEN sibling pattern not applying here). The RED fixtures below cover
// the collapse / extra-method / return-type regressions.
func TestRegistrySubscribeCellIDMandatory_BuilderShape(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "framework", "kernel", "cell", "subscription_builder.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, v := range collectSubscriptionBuilderShapeViolations(fset, f, "kernel/cell/subscription_builder.go") {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01: %s", v)
	}
}

// TestRegistrySubscribeCellIDMandatory_BuilderShape_DetectorFixtures is the
// RED-fixture self-check for prong 2. It drives
// collectSubscriptionBuilderShapeViolations against synthetic builder
// declarations so a regression that stops detecting a collapsed/leaky builder
// shape fails here.
func TestRegistrySubscribeCellIDMandatory_BuilderShape_DetectorFixtures(t *testing.T) {
	t.Parallel()
	const greenBuilder = `
func (b *subscriptionBuilder) ConsumerGroup(g string) *subscriptionBuilder { return b }
func (b *subscriptionBuilder) Handler(h Handler) *subscriptionBuilder { return b }
func (b *subscriptionBuilder) SliceID(s string) *subscriptionBuilder { return b }
func (b *subscriptionBuilder) Register() error { return nil }
`
	cases := []struct {
		name           string
		src            string
		wantViolations bool
	}{
		{
			name: "green_canonical",
			src: `package fixture
type subscriptionDraft struct{}
type subscriptionBuilder struct{}
func (d *subscriptionDraft) CellID(id string) *subscriptionBuilder { return nil }` + greenBuilder,
		},
		{
			// All methods collapsed onto one type with an optional CellID — the
			// regression the prong exists to stop. Draft missing → CellID missing
			// on Draft AND an unexpected CellID on Builder.
			name: "red_collapsed_single_type",
			src: `package fixture
type subscriptionBuilder struct{}
func (b *subscriptionBuilder) CellID(id string) *subscriptionBuilder { return b }` + greenBuilder,
			wantViolations: true,
		},
		{
			name: "red_draft_has_register",
			src: `package fixture
type subscriptionDraft struct{}
type subscriptionBuilder struct{}
func (d *subscriptionDraft) CellID(id string) *subscriptionBuilder { return nil }
func (d *subscriptionDraft) Register() error { return nil }` + greenBuilder,
			wantViolations: true,
		},
		{
			name: "red_cellid_returns_draft",
			src: `package fixture
type subscriptionDraft struct{}
type subscriptionBuilder struct{}
func (d *subscriptionDraft) CellID(id string) *subscriptionDraft { return d }` + greenBuilder,
			wantViolations: true,
		},
		{
			name: "red_builder_missing_register",
			src: `package fixture
type subscriptionDraft struct{}
type subscriptionBuilder struct{}
func (d *subscriptionDraft) CellID(id string) *subscriptionBuilder { return nil }
func (b *subscriptionBuilder) ConsumerGroup(g string) *subscriptionBuilder { return b }
func (b *subscriptionBuilder) Handler(h Handler) *subscriptionBuilder { return b }
func (b *subscriptionBuilder) SliceID(s string) *subscriptionBuilder { return b }`,
			wantViolations: true,
		},
		{
			name: "red_draft_extra_method",
			src: `package fixture
type subscriptionDraft struct{}
type subscriptionBuilder struct{}
func (d *subscriptionDraft) CellID(id string) *subscriptionBuilder { return nil }
func (d *subscriptionDraft) Sneak() error { return nil }` + greenBuilder,
			wantViolations: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, fset := parseSubscriptionFixtureSrc(t, tc.src)
			violations := collectSubscriptionBuilderShapeViolations(fset, f, "fixture.go")
			if got := len(violations) > 0; got != tc.wantViolations {
				t.Errorf("violations non-empty = %v (%v), want %v", got, violations, tc.wantViolations)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01 (prong 2: builder field-set freeze)
// ---------------------------------------------------------------------------

// subscriptionStepFieldSets is the frozen field set of each sealed step type.
// The recorder back-reference + contract spec + the accumulated subscription
// fields are the ENTIRE legitimate state; anything else is drift.
var subscriptionStepFieldSets = map[string][]string{
	subDraftType:   {"reg", "spec"},
	subBuilderType: {"reg", "spec", "cellID", "consumerGroup", "sliceID", "handler"},
}

// diffStringSet returns one message per missing or unexpected member, freezing
// got to exactly want.
func diffStringSet(got, want []string) []string {
	wantSet := make(map[string]struct{}, len(want))
	for _, w := range want {
		wantSet[w] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, g := range got {
		gotSet[g] = struct{}{}
	}
	var msgs []string
	for g := range gotSet {
		if _, ok := wantSet[g]; !ok {
			msgs = append(msgs, fmt.Sprintf("unexpected field %q", g))
		}
	}
	for _, w := range want {
		if _, ok := gotSet[w]; !ok {
			msgs = append(msgs, fmt.Sprintf("missing field %q", w))
		}
	}
	return msgs
}

// collectSubscriptionBuilderFieldViolations freezes the two step-type struct
// field sets and forbids embedded (unnamed) fields. This closes the
// embedded-promotion blind spot that the method-set freeze
// (collectSubscriptionBuilderShapeViolations) cannot see: an embedded type that
// PROMOTES a Register method onto subscriptionDraft has no FuncDecl with a draft
// receiver in this file, so the method-set scan misses it — but it appears here
// as an unnamed field and is rejected, keeping the missing-CellID path
// unreachable.
//
// Detector blind spots (vs scanner.EachInSubtree[ast.TypeSpec] + struct field
// AST): field TYPES are not inspected (only names + embedded-ness), which is
// sufficient — promotion requires an embedded (unnamed) field regardless of its
// type, and a renamed/extra/missing named field is caught by the set diff. The
// RED fixtures below cover embed / extra-field / missing-field regressions.
func collectSubscriptionBuilderFieldViolations(f *ast.File, label string) []string {
	got := map[string][]string{}
	embedded := map[string]bool{}
	seen := map[string]bool{}
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		name := ts.Name.Name
		if name != subDraftType && name != subBuilderType {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return
		}
		seen[name] = true
		if st.Fields == nil {
			return
		}
		for _, fld := range st.Fields.List {
			if len(fld.Names) == 0 {
				embedded[name] = true
				continue
			}
			for _, n := range fld.Names {
				got[name] = append(got[name], n.Name)
			}
		}
	})

	var viol []string
	for _, typeName := range []string{subDraftType, subBuilderType} {
		if !seen[typeName] {
			viol = append(viol, fmt.Sprintf("%s: struct %s not found (field-set freeze)", label, typeName))
			continue
		}
		if embedded[typeName] {
			viol = append(viol, fmt.Sprintf("%s: %s must not embed any type — an embedded field can promote a "+
				"method (e.g. Register) onto the step type and reopen the missing-CellID path", label, typeName))
		}
		for _, m := range diffStringSet(got[typeName], subscriptionStepFieldSets[typeName]) {
			viol = append(viol, fmt.Sprintf("%s: %s field set frozen to %v — %s", label, typeName,
				subscriptionStepFieldSets[typeName], m))
		}
	}
	return viol
}

// TestRegistrySubscribeCellIDMandatory_BuilderFields enforces prong 2 on the
// FIELD axis: the two sealed step types expose exactly their legitimate fields
// and embed nothing. Method-set freezing alone cannot see an embedded type that
// promotes Register onto the draft; freezing the field sets — and banning
// embedded fields outright — keeps the missing-CellID path unexpressible.
func TestRegistrySubscribeCellIDMandatory_BuilderFields(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "framework", "kernel", "cell", "subscription_builder.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, v := range collectSubscriptionBuilderFieldViolations(f, "kernel/cell/subscription_builder.go") {
		t.Errorf("REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01: %s", v)
	}
}

// TestRegistrySubscribeCellIDMandatory_BuilderFields_DetectorFixtures is the
// RED-fixture self-check for the field-freeze collector: a regression that stops
// detecting an embed / extra-field / missing-field drift fails here.
func TestRegistrySubscribeCellIDMandatory_BuilderFields_DetectorFixtures(t *testing.T) {
	t.Parallel()
	const greenBuilderStruct = `
type subscriptionBuilder struct {
	reg *RegistryRecorder
	spec contractspec.ContractSpec
	cellID string
	consumerGroup string
	sliceID string
	handler outbox.EntryHandler
}`
	cases := []struct {
		name           string
		src            string
		wantViolations bool
	}{
		{
			name: "green_canonical",
			src: `package fixture
type subscriptionDraft struct { reg *RegistryRecorder; spec contractspec.ContractSpec }` + greenBuilderStruct,
		},
		{
			// Embedded type on the draft can promote a Register method — the exact
			// blind spot the method-set freeze cannot see.
			name: "red_draft_embeds_type",
			src: `package fixture
type subscriptionDraft struct { *RegistryRecorder; spec contractspec.ContractSpec }` + greenBuilderStruct,
			wantViolations: true,
		},
		{
			name: "red_builder_extra_field",
			src: `package fixture
type subscriptionDraft struct { reg *RegistryRecorder; spec contractspec.ContractSpec }
type subscriptionBuilder struct {
	reg *RegistryRecorder
	spec contractspec.ContractSpec
	cellID string
	consumerGroup string
	sliceID string
	handler outbox.EntryHandler
	sneak string
}`,
			wantViolations: true,
		},
		{
			name: "red_draft_missing_field",
			src: `package fixture
type subscriptionDraft struct { reg *RegistryRecorder }` + greenBuilderStruct,
			wantViolations: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, _ := parseSubscriptionFixtureSrc(t, tc.src)
			viol := collectSubscriptionBuilderFieldViolations(f, "fixture.go")
			if got := len(viol) > 0; got != tc.wantViolations {
				t.Errorf("violations non-empty = %v (%v), want %v", got, viol, tc.wantViolations)
			}
		})
	}
}
