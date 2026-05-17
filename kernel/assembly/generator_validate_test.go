package assembly

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/scaffoldid"
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

// ID and Cells pattern coverage moved to kernel/scaffoldid/scaffoldid_test.go
// TestParse_Reject / TestParse_Accept: scaffoldid.Parse is the SINGLE source
// of identifier-pattern validation, and AssemblyScaffoldSpec.ID /
// AssemblyScaffoldSpec.Cells are typed (scaffoldid.ScaffoldID) so the
// AssemblyIDPattern constraint is established at construction time
// (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01). The legacy
// TestValidateAssemblyScaffoldSpec_IDMetadataRule /
// _CellsMetadataRule tests are no longer applicable — their cases live in
// scaffoldid_test.go.

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
				ID:        mustID(t, "myassembly"),
				Cells:     []scaffoldid.ScaffoldID{scaffoldid.MustParse("examplecell")},
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
