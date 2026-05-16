package assembly

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// projectWithCell builds a minimal ProjectMeta containing one cell named id,
// used to satisfy validateAssemblyScaffoldSpec cell-existence pre-condition
// when the test focuses on ID/text metadata rules instead of cell existence.
func projectWithCell(id string) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			id: {
				ID:               id,
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: id},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + id}},
			},
		},
	}
}

// TestValidateAssemblyScaffoldSpec_IDMetadataRule asserts that
// validateAssemblyScaffoldSpec routes spec.ID through the single source
// metadata.MatchAssemblyID — rejecting kebab-case, capitalised, digit-leading,
// short, and other shapes that the AssemblyIDPattern (^[a-z][a-z0-9]+$) does
// not match. This is the SCAFFOLD-ASSEMBLY-ID-METADATA-RULE-01 RED gate.
//
// ref: kubernetes/apimachinery pkg/util/validation/validation.go — same
// single-helper IsDNS1123Label pattern applied here for assembly ID.
func TestValidateAssemblyScaffoldSpec_IDMetadataRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		id        string
		wantValid bool
	}{
		{"valid_assembly_id", "myassembly", true},
		{"valid_letters_then_digits", "asm9", true},

		// Pattern violations (orthogonal to path-traversal — these are not
		// rejected by the legacy validateAssemblyPathComponent character
		// blacklist but must now be rejected by the metadata pattern).
		{"kebab_rejected", "my-assembly", false},
		{"capitalised_rejected", "MyAssembly", false},
		{"digit_start_rejected", "9myassembly", false},
		{"single_char_rejected", "a", false},
		{"underscore_rejected", "my_assembly", false},
		{"trailing_dash_rejected", "myasm-", false},

		// Control-char path also must remain rejected (the metadata pattern
		// is a strict superset of path/control-char rejection).
		{"newline_rejected", "my\nassembly", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			project := projectWithCell("examplecell")
			gen := NewGenerator(project, "github.com/ghbvf/gocell", t.TempDir())
			spec := AssemblyScaffoldSpec{
				ID:        tc.id,
				Cells:     []string{"examplecell"},
				OwnerTeam: "platform",
				OwnerRole: "maintainer",
			}

			err := validateAssemblyScaffoldSpec(gen, spec)
			gotValid := err == nil
			if gotValid != tc.wantValid {
				t.Fatalf("validateAssemblyScaffoldSpec(ID=%q) valid=%v err=%v; want valid=%v",
					tc.id, gotValid, err, tc.wantValid)
			}
			if !tc.wantValid {
				assertInvalidErrcode(t, err)
			}
		})
	}
}

// TestValidateAssemblyScaffoldSpec_CellsMetadataRule asserts that every
// element of spec.Cells routes through metadata.MatchCellID. The legacy
// validateAssemblyPathComponent accepted kebab-case cell IDs that the
// CellIDPattern (^[a-z][a-z0-9]+$) rejects; this gate locks in the upgrade.
func TestValidateAssemblyScaffoldSpec_CellsMetadataRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		cells     []string
		wantValid bool
	}{
		{"valid_cell", []string{"examplecell"}, true},
		{"multiple_valid_cells", []string{"examplecell", "examplecell"}, true},
		{"kebab_rejected", []string{"my-cell"}, false},
		{"capitalised_rejected", []string{"MyCell"}, false},
		{"digit_start_rejected", []string{"9cell"}, false},
		{"second_cell_invalid", []string{"examplecell", "9bad"}, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			project := projectWithCell("examplecell")
			gen := NewGenerator(project, "github.com/ghbvf/gocell", t.TempDir())
			spec := AssemblyScaffoldSpec{
				ID:        "myassembly",
				Cells:     tc.cells,
				OwnerTeam: "platform",
				OwnerRole: "maintainer",
			}

			err := validateAssemblyScaffoldSpec(gen, spec)
			gotValid := err == nil
			if gotValid != tc.wantValid {
				t.Fatalf("validateAssemblyScaffoldSpec(Cells=%v) valid=%v err=%v; want valid=%v",
					tc.cells, gotValid, err, tc.wantValid)
			}
		})
	}
}

// TestValidateAssemblyScaffoldSpec_OwnerTextRule asserts that OwnerTeam /
// OwnerRole free-text fields route through metadata.IsValidMetadataText
// — rejecting only \n / \r / \x00 control characters that would break
// YAML scalar emission. Other characters (colons, dashes, unicode) are
// accepted at this layer; full YAML scalar safety is delegated to
// pkg/yamlsafe.Quote at the rendering boundary.
func TestValidateAssemblyScaffoldSpec_OwnerTextRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		team      string
		role      string
		wantValid bool
	}{
		{"ascii_text", "platform", "maintainer", true},
		{"unicode_text", "技术平台", "维护者", true},
		{"dashed_text", "site-reliability", "team-lead", true},
		{"colon_accepted", "team:platform", "role:owner", true},

		// Control characters rejected.
		{"team_lf_rejected", "alice\nbob", "maintainer", false},
		{"team_cr_rejected", "alice\rbob", "maintainer", false},
		{"team_nul_rejected", "alice\x00bob", "maintainer", false},
		{"role_lf_rejected", "platform", "evil\nrole", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			project := projectWithCell("examplecell")
			gen := NewGenerator(project, "github.com/ghbvf/gocell", t.TempDir())
			spec := AssemblyScaffoldSpec{
				ID:        "myassembly",
				Cells:     []string{"examplecell"},
				OwnerTeam: tc.team,
				OwnerRole: tc.role,
			}

			err := validateAssemblyScaffoldSpec(gen, spec)
			gotValid := err == nil
			if gotValid != tc.wantValid {
				t.Fatalf("validateAssemblyScaffoldSpec(team=%q role=%q) valid=%v err=%v; want valid=%v",
					tc.team, tc.role, gotValid, err, tc.wantValid)
			}
		})
	}
}

// assertInvalidErrcode checks that err is a KindInvalid errcode error.
// Error message wording is not asserted; that's a documentation concern,
// not a behavioral one. Only KindInvalid is a behavioral invariant.
func assertInvalidErrcode(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInvalid {
		t.Fatalf("expected KindInvalid, got %q (msg=%s)", ec.Kind, ec.Message)
	}
}
