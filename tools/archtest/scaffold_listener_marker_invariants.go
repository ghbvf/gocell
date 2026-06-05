// Importable rule body for SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01. Migrated
// from the deleted scaffold_bundle_invariants_test.go (M3 #1302). The dogfood +
// template precision gate live in scaffold_listener_marker_invariants_test.go.
//
// # SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01
//
// cellgen.ListenerMarker must exist as an exported string const carrying the
// canonical K#05 marker literal, and the scaffold-cell template must reference
// it via {{.ListenerMarker}} rather than hand-typing the literal. This keeps the
// marker→cell.yaml drift guard (MARKERGEN-DRIFT-VERIFY-01) anchored to a single
// typed-const source instead of a free-floating string.
//
// # AI-robust: Medium (typed const + typeseval cross-validation)
//
// The const half resolves cellgen.ListenerMarker via the typed Run façade and
// compares its constant.StringVal; the template half is a pure string check
// (checkListenerTemplate) that the precision gate table-tests directly.
//
// # Scope: gocell-internal-layout
//
// It scans the platform's own tools/codegen/cellgen package + the
// templates/scaffold-cell.tmpl asset, neither of which exists in an external
// Cell repo — so it is NOT enrolled in StandardCellRules (it would be
// vacuous-green there). It gets the unified PlatformModulePath parameterization
// (ARCHTEST-MODULE-PATH-FUNNEL-01) + fork-safety only.
package archtest

import (
	"errors"
	"fmt"
	"go/constant"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ruleScaffoldListenerMarkerTypedConst01 = "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01"

const (
	// cellgenPkgPath is the import path of the scaffold generator package,
	// derived from PlatformModulePath so a module rename updates one place.
	cellgenPkgPath        = PlatformModulePath + "/tools/codegen/cellgen"
	listenerMarkerConst   = "ListenerMarker"
	listenerMarkerLiteral = "// +cell:listener:"
	listenerMarkerTmplRef = "{{.ListenerMarker}}"
	// scaffoldCellTmplRel is a running-module repo-relative asset path (not a
	// platform module-path literal); absent in an external repo → vacuous.
	scaffoldCellTmplRel = "tools/codegen/cellgen/templates/scaffold-cell.tmpl"
)

// CheckScaffoldListenerMarkerTypedConst enforces SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01:
// it verifies the cellgen.ListenerMarker typed const and the scaffold-cell
// template's use of {{.ListenerMarker}}, returning the diagnostics it observes.
func CheckScaffoldListenerMarkerTypedConst(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	out := checkListenerMarkerConst(t, cfg)
	root := findModuleRoot(t)
	//nolint:gosec // repo-relative asset path, not user-supplied
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(scaffoldCellTmplRel)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Template absent (external Cell repo) → nothing to validate (vacuous).
		return out
	case err != nil:
		// A genuine read error in GoCell's own tree (permission / IO) must not be
		// swallowed — surface it so the dogfood fails loud instead of silently
		// dropping the template check.
		return append(out, Diagnostic{
			Rel:     scaffoldCellTmplRel,
			Line:    0,
			Message: "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: failed to read scaffold-cell template: " + err.Error(),
		})
	}
	return append(out, checkListenerTemplate(scaffoldCellTmplRel, string(content))...)
}

// checkListenerMarkerConst verifies cellgen.ListenerMarker is an exported string
// const equal to the canonical marker literal, via the typed Run façade. If the
// cellgen package is not in scope (external Cell repo, or no cellgen), there is
// nothing to verify — it returns no diagnostics (vacuous), symmetric with an
// absent template.
func checkListenerMarkerConst(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	var out []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tags: cfg.BuildTags}, []string{"./tools/codegen/cellgen/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != cellgenPkgPath {
				return nil
			}
			out = append(out, listenerMarkerConstDiags(p)...)
			return nil
		})
	return out
}

// listenerMarkerConstDiags resolves the ListenerMarker const object in the
// loaded cellgen package and validates it is an exported string const equal to
// the canonical literal.
func listenerMarkerConstDiags(p *Pass) []Diagnostic {
	obj := p.Pkg.Scope().Lookup(listenerMarkerConst)
	if obj == nil {
		return []Diagnostic{{
			Rel: cellgenPkgPath, Line: 0,
			Message: "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: exported const " + listenerMarkerConst +
				" not found in " + cellgenPkgPath +
				"; add: const ListenerMarker = \"" + listenerMarkerLiteral + "\"",
		}}
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return []Diagnostic{{
			Rel: cellgenPkgPath, Line: 0,
			Message: fmt.Sprintf("SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: cellgen.%s is not a const (got %T)",
				listenerMarkerConst, obj),
		}}
	}
	if c.Val().Kind() != constant.String {
		return []Diagnostic{{
			Rel: cellgenPkgPath, Line: 0,
			Message: "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: cellgen." + listenerMarkerConst +
				" is not a string const",
		}}
	}
	if v := constant.StringVal(c.Val()); v != listenerMarkerLiteral {
		return []Diagnostic{{
			Rel: cellgenPkgPath, Line: 0,
			Message: fmt.Sprintf("SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: cellgen.%s = %q; want %q",
				listenerMarkerConst, v, listenerMarkerLiteral),
		}}
	}
	return nil
}

// checkListenerTemplate verifies the scaffold-cell template references the typed
// const via {{.ListenerMarker}} and does NOT hand-type the bare marker literal.
// It is a pure function so the precision gate can table-test it directly.
func checkListenerTemplate(rel, content string) []Diagnostic {
	var out []Diagnostic
	if !strings.Contains(content, listenerMarkerTmplRef) {
		out = append(out, Diagnostic{
			Rel: rel, Line: 0,
			Message: "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: scaffold-cell.tmpl does not reference " +
				listenerMarkerTmplRef + "; the template must use the typed-const funnel instead of a " +
				"hand-typed literal",
		})
	}
	if strings.Contains(content, listenerMarkerLiteral) {
		out = append(out, Diagnostic{
			Rel: rel, Line: 0,
			Message: "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: scaffold-cell.tmpl contains literal " +
				listenerMarkerLiteral + " outside " + listenerMarkerTmplRef +
				"; remove the literal and rely solely on the typed-const reference",
		})
	}
	// ListenerMarker already includes the `// ` comment lead, so prefixing the
	// template reference with `// ` renders a double-commented
	// `// // +cell:listener:` that markergen.splitMarker (which requires a `// +`
	// prefix) does NOT recognize — a silently broken marker in scaffold output.
	if strings.Contains(content, "// "+listenerMarkerTmplRef) {
		out = append(out, Diagnostic{
			Rel: rel, Line: 0,
			Message: "SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: scaffold-cell.tmpl prefixes " +
				listenerMarkerTmplRef + " with `// ` — ListenerMarker already carries the comment " +
				"lead, so this renders a double-commented `// // +cell:listener:` that markergen does " +
				"not parse; drop the leading `// `",
		})
	}
	return out
}
