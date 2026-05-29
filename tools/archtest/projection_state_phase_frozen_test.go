// INVARIANT: PROJECTION-STATE-PHASE-FROZEN-01
//
// PROJECTION-STATE-PHASE-FROZEN-01 — the rebuild-lifecycle Phase enum declared
// in kernel/projection/phase.go is frozen to exactly five members
// {PhaseLive, PhaseStopped, PhaseReset, PhaseReplay, PhaseCatchup} with the
// canonical wire/log String() mapping. The set is the operational contract for
// readyz / metrics / the business read-503 opt-in (Phase()); silently adding,
// removing, or renaming a phase, or changing a String() literal, drifts the
// contract. This lock fires on any such drift so the change is a deliberate,
// reviewed golden update (bump the expected set below in the same PR).
//
// The enum is declared in PR-00 (#1172, ADR
// docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md); the
// rebuild state machine + transition table that consume it land in PR-03. This
// archtest is green from PR-00 onward — it locks membership, not behavior.
//
// AI-robust 评级：Medium (AST const-set + String-arm lock).
//   - This is stronger than a bare string-anchor: it parses the const block
//     structurally and the String() switch arms, so it is not fooled by the
//     word "Phase" appearing elsewhere. It is NOT Hard: discovery keys on the
//     literal name "PhaseLive" to locate the const block, and Go enums are not
//     reflectable as a set (unlike struct fields under
//     SUBSCRIBERS-DERIVED-FIELD-FROZEN-01), so a golden/AST lock is the ceiling
//     for an enum membership freeze. The type system already carries the
//     orthogonal "zero value is invalid" guarantee via iota+1 + Phase.Valid().
//   - Sibling precedent: OUTBOX-STATE-TRANSITION-COMPLETENESS-01 locks an enum
//     transition map's key set; this locks the const set + String arms.
//
// 盲区自检（所选工具 go/parser AST walk 的声明范围外形态）:
//   - The const-block locator keys on the member name "PhaseLive". Renaming
//     PhaseLive itself surfaces as a "block not found" Fatal (visible), not a
//     silent pass — asserted by the reverse self-check below.
//   - The String-arm extractor only reads `case PhaseX: return "literal"` arms
//     (const-Ident cases). A String() rewritten to compute the literal
//     dynamically (e.g. via a map or fmt) yields an empty arm map and fails the
//     per-member assertion — visible, not silent; exercised by reverse
//     self-check case (4). The `case 0: return "invalid"` arm is keyed by an
//     integer literal (BasicLit), not a const Ident, so it is locked separately
//     via phaseZeroLiteralArm (reverse self-check case (5)).
//   - Pointer-receiver String(): the locator accepts both `Phase` and `*Phase`
//     receivers; value receiver is the established convention.
//
// ref: kernel/outbox/state.go (enum + String + transition-table pattern).
// ref: tools/archtest/contract_subscribers_funnel_test.go (frozen-field precedent).
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// wantPhaseConsts is the frozen, ordered membership of the Phase enum.
// Changing this requires a same-PR review (golden update).
var wantPhaseConsts = []string{
	"PhaseLive",
	"PhaseStopped",
	"PhaseReset",
	"PhaseReplay",
	"PhaseCatchup",
}

// wantPhaseStrings is the frozen const→wire/log string mapping from String().
var wantPhaseStrings = map[string]string{
	"PhaseLive":    "live",
	"PhaseStopped": "stopped",
	"PhaseReset":   "reset",
	"PhaseReplay":  "replay",
	"PhaseCatchup": "catchup",
}

// TestProjectionStatePhaseFrozen01 locks the Phase enum membership + String arms.
func TestProjectionStatePhaseFrozen01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	phasePath := filepath.Clean(filepath.Join(root, "kernel", "projection", "phase.go"))
	src, err := os.ReadFile(phasePath)
	if err != nil {
		t.Fatalf("PROJECTION-STATE-PHASE-FROZEN-01: read phase.go: %v", err)
	}

	names, arms, err := parsePhaseEnum(src)
	if err != nil {
		t.Fatalf("PROJECTION-STATE-PHASE-FROZEN-01: parse phase.go: %v", err)
	}

	if !reflect.DeepEqual(names, wantPhaseConsts) {
		t.Errorf("PROJECTION-STATE-PHASE-FROZEN-01: Phase const set = %v, want %v "+
			"(adding/removing/renaming/reordering a phase changes the operational "+
			"contract — update wantPhaseConsts in the same PR if intentional)", names, wantPhaseConsts)
	}

	if !reflect.DeepEqual(arms, wantPhaseStrings) {
		t.Errorf("PROJECTION-STATE-PHASE-FROZEN-01: Phase String() arms = %v, want %v "+
			"(String() literals are the wire/log contract — update wantPhaseStrings if intentional)",
			arms, wantPhaseStrings)
	}

	// The zero-value arm (`case 0: return "invalid"`) is also a wire/log
	// contract value; lock it explicitly since it is a BasicLit case, not an
	// Ident case (and thus excluded from the const-keyed arm map above).
	if zero := phaseZeroLiteralArm(parseFileOrFatal(t, src)); zero != "invalid" {
		t.Errorf("PROJECTION-STATE-PHASE-FROZEN-01: Phase String() `case 0:` returns %q, want %q",
			zero, "invalid")
	}
}

// TestProjectionStatePhaseFrozen01_ReverseSelfCheck proves the parser has teeth:
// it detects an added member, a removed member, and a missing String arm, and it
// Fatals (not silently passes) when the locator name is absent.
func TestProjectionStatePhaseFrozen01_ReverseSelfCheck(t *testing.T) {
	t.Parallel()

	// (1) An added member must be observed (6 names, not 5).
	added := []byte(`package projection
type Phase uint8
const (
	PhaseLive Phase = iota + 1
	PhaseStopped
	PhaseReset
	PhaseReplay
	PhaseCatchup
	PhaseExtra
)
func (p Phase) String() string {
	switch p {
	case PhaseLive:
		return "live"
	}
	return "x"
}
`)
	names, _, err := parsePhaseEnum(added)
	if err != nil {
		t.Fatalf("reverse self-check (added): parse: %v", err)
	}
	if len(names) != 6 || names[5] != "PhaseExtra" {
		t.Errorf("reverse self-check: parser did not observe an added const; got %v", names)
	}

	// (2) A missing String arm must be observed (no "PhaseReset" key).
	missingArm := []byte(`package projection
type Phase uint8
const (
	PhaseLive Phase = iota + 1
	PhaseReset
)
func (p Phase) String() string {
	switch p {
	case PhaseLive:
		return "live"
	}
	return "x"
}
`)
	_, arms, err := parsePhaseEnum(missingArm)
	if err != nil {
		t.Fatalf("reverse self-check (missing arm): parse: %v", err)
	}
	if _, present := arms["PhaseReset"]; present {
		t.Error("reverse self-check: parser invented a String arm for PhaseReset that does not exist")
	}
	if arms["PhaseLive"] != "live" {
		t.Errorf("reverse self-check: parser dropped a real arm; got %v", arms)
	}

	// (3) Absent locator name → block-not-found error (visible, not silent pass).
	noBlock := []byte(`package projection
type Phase uint8
const Renamed Phase = 1
func (p Phase) String() string { return "x" }
`)
	if _, _, err := parsePhaseEnum(noBlock); err == nil {
		t.Error("reverse self-check: parser did not error when the PhaseLive const block is absent")
	}

	// (4) A map-based (dynamic) String() has no case arms, so the arm map comes
	// back empty and the main per-member assertion fails visibly rather than
	// passing silently — exercising the documented dynamic-String blind spot.
	dynamicString := []byte(`package projection
type Phase uint8
const (
	PhaseLive Phase = iota + 1
	PhaseStopped
	PhaseReset
	PhaseReplay
	PhaseCatchup
)
var names = map[Phase]string{PhaseLive: "live"}
func (p Phase) String() string { return names[p] }
`)
	_, dynArms, err := parsePhaseEnum(dynamicString)
	if err != nil {
		t.Fatalf("reverse self-check (dynamic String): parse: %v", err)
	}
	if len(dynArms) != 0 {
		t.Errorf("reverse self-check: parser found case arms in a map-based String(); got %v", dynArms)
	}

	// (5) The zero-value arm extractor finds `case 0:` and ignores a renamed
	// literal — proving a change away from "invalid" is observable.
	if got := phaseZeroLiteralArm(parseFileOrFatal(t, []byte(`package projection
type Phase uint8
const PhaseLive Phase = iota + 1
func (p Phase) String() string {
	switch p {
	case 0:
		return "unknown"
	}
	return "x"
}
`))); got != "unknown" {
		t.Errorf("reverse self-check: phaseZeroLiteralArm = %q, want %q (must observe the real literal)", got, "unknown")
	}
}

// parsePhaseEnum extracts the ordered Phase const names (from the const block
// that declares "PhaseLive") and the const→string map from the String() method's
// `case PhaseX: return "literal"` arms. Returns an error if the const block is
// not found (locator name renamed / removed).
func parsePhaseEnum(src []byte) (names []string, arms map[string]string, err error) {
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, "phase.go", src, 0)
	if perr != nil {
		return nil, nil, perr
	}

	names = phaseConstNames(f)
	if names == nil {
		return nil, nil, errPhaseBlockNotFound
	}
	arms = phaseStringArms(f)
	return names, arms, nil
}

var errPhaseBlockNotFound = phaseErr("PhaseLive const block not found")

type phaseErr string

func (e phaseErr) Error() string { return string(e) }

// phaseConstNames returns the ordered const names in the GenDecl that declares
// PhaseLive, or nil if no such block exists.
func phaseConstNames(f *ast.File) []string {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		var block []string
		hasLocator := false
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				block = append(block, n.Name)
				if n.Name == "PhaseLive" {
					hasLocator = true
				}
			}
		}
		if hasLocator {
			return block
		}
	}
	return nil
}

// phaseStringArms maps each `case <Ident>: return "<literal>"` arm in the
// Phase.String() method to its string literal.
func phaseStringArms(f *ast.File) map[string]string {
	arms := map[string]string{}
	WalkFuncDeclsAST([]*ast.File{f}, nil, nil, func(ctx FuncDeclContext) {
		if ctx.Func.Name.Name != "String" || !HasReceiver(ctx.Func, "Phase") {
			return
		}
		EachInSubtree[ast.CaseClause](ctx.Func.Body, func(cc *ast.CaseClause) {
			lit := firstReturnedStringLit(cc.Body)
			if lit == "" {
				return
			}
			for _, e := range cc.List {
				if id, ok := e.(*ast.Ident); ok {
					arms[id.Name] = lit
				}
			}
		})
	})
	return arms
}

func parseFileOrFatal(t *testing.T, src []byte) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "phase.go", src, 0)
	if err != nil {
		t.Fatalf("PROJECTION-STATE-PHASE-FROZEN-01: parse: %v", err)
	}
	return f
}

// phaseZeroLiteralArm returns the string literal of the `case 0:` arm in
// Phase.String(), or "" if absent. This is the only arm keyed by an integer
// literal (BasicLit), so phaseStringArms (which keys on const Idents) excludes
// it; it is locked separately because "invalid" is also a wire/log contract.
func phaseZeroLiteralArm(f *ast.File) string {
	var lit string
	WalkFuncDeclsAST([]*ast.File{f}, nil, nil, func(ctx FuncDeclContext) {
		if lit != "" || ctx.Func.Name.Name != "String" || !HasReceiver(ctx.Func, "Phase") {
			return
		}
		EachInSubtree[ast.CaseClause](ctx.Func.Body, func(cc *ast.CaseClause) {
			for _, e := range cc.List {
				if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.INT && bl.Value == "0" {
					lit = firstReturnedStringLit(cc.Body)
				}
			}
		})
	})
	return lit
}

func firstReturnedStringLit(body []ast.Stmt) string {
	for _, s := range body {
		ret, ok := s.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		lit, ok := ret.Results[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		return v
	}
	return ""
}
