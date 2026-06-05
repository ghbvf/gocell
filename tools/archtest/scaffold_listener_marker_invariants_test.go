// INVARIANT: SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01
//
// This _test.go dogfoods the rule against GoCell's own cellgen package +
// scaffold-cell template, and unit-tests the pure template-content helper
// checkListenerTemplate as the precision gate. The importable rule body
// (CheckScaffoldListenerMarkerTypedConst + checkListenerTemplate + consts)
// lives in the non-test companion scaffold_listener_marker_invariants.go.
//
// Migrated from the deleted scaffold_bundle_invariants_test.go (M3 #1302). It
// is gocell-internal-layout — it scans the platform's own
// tools/codegen/cellgen package + templates/scaffold-cell.tmpl, which do not
// exist in an external Cell repo — so it is NOT enrolled in StandardCellRules
// (it would be vacuous-green there); it gets the unified PlatformModulePath
// parameterization + fork-safety only.
package archtest

import "testing"

// TestScaffoldListenerMarkerTypedConst dogfoods SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01
// against GoCell itself: cellgen.ListenerMarker must be an exported string const
// with the canonical K#05 marker value, and templates/scaffold-cell.tmpl must
// reference it via {{.ListenerMarker}} rather than hand-typing the literal.
func TestScaffoldListenerMarkerTypedConst(t *testing.T) {
	t.Parallel()
	Report(t, ruleScaffoldListenerMarkerTypedConst01,
		CheckScaffoldListenerMarkerTypedConst(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestCheckListenerTemplate_PrecisionGate is the RED/GREEN precision gate for
// the template-content half of SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01. It
// feeds checkListenerTemplate synthetic template bodies and asserts the exact
// diagnostic count, proving the funnel rejects a missing {{.ListenerMarker}}
// reference AND a hand-typed bare marker literal.
func TestCheckListenerTemplate_PrecisionGate(t *testing.T) {
	t.Parallel()
	const rel = "tools/codegen/cellgen/templates/scaffold-cell.tmpl"
	cases := []struct {
		name      string
		content   string
		wantDiags int
	}{
		// GREEN: references the typed const at line start (no `// ` lead, since
		// ListenerMarker already carries the comment lead), no hand-typed literal.
		{"green_typed_ref", "package {{.Package}}\n{{.ListenerMarker}}ref=cell.PrimaryListener\n", 0},
		// RED: missing the {{.ListenerMarker}} reference entirely.
		{"red_missing_ref", "package {{.Package}}\n", 1},
		// RED: hand-typed bare marker literal AND missing the typed reference.
		{"red_handtyped_only", "// +cell:listener:\n", 2},
		// RED: has the typed reference but ALSO hand-types the bare literal.
		{"red_handtyped_plus_ref", "{{.ListenerMarker}}\n// +cell:listener:\n", 1},
		// RED: double-commented `// {{.ListenerMarker}}` renders the broken
		// `// // +cell:listener:` marker (ref present, no bare literal → 1 diag).
		{"red_double_comment", "package {{.Package}}\n// {{.ListenerMarker}}ref=x\n", 1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkListenerTemplate(rel, tc.content)
			if len(got) != tc.wantDiags {
				t.Errorf("checkListenerTemplate(%s) = %d diags, want %d:\n%+v",
					tc.name, len(got), tc.wantDiags, got)
			}
		})
	}
}
