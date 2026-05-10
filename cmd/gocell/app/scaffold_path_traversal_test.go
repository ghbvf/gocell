package app

import (
	"strings"
	"testing"
)

// TestRunScaffold_PathTraversal verifies that all four scaffold subcommands
// reject identifiers containing path traversal sequences after the K#09
// kernel/scaffold deletion. Each row exercises one (subcommand, flag) pair
// against a rotating set of attack payloads.
func TestRunScaffold_PathTraversal(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	payloads := []string{"../escape", "/abs", `\\back`, "..", "."}
	cases := []struct {
		name     string
		baseArgs []string
		flag     string // identifier flag to mutate
	}{
		{"slice_id", []string{"slice", "--cell=examplecell"}, "--id"},
		{"slice_cell", []string{"slice", "--id=foo"}, "--cell"},
		{"contract_id", []string{"contract", "--kind=http", "--owner=examplecell"}, "--id"},
		{"contract_owner", []string{"contract", "--id=http.foo.bar.v1", "--kind=http"}, "--owner"},
		{"journey_id", []string{"journey", "--goal=g", "--team=t", "--cells=examplecell"}, "--id"},
		{"assembly_id", []string{"assembly", "--cells=examplecell", "--team=t", "--role=r"}, "--id"},
	}

	for _, tc := range cases {
		tc := tc
		for _, payload := range payloads {
			payload := payload
			t.Run(tc.name+"_"+strings.ReplaceAll(strings.ReplaceAll(payload, "/", "_slash_"), `\`, "_bs_"), func(t *testing.T) {
				t.Parallel()
				args := append([]string{}, tc.baseArgs...)
				args = append(args, tc.flag+"="+payload)
				err := runScaffoldWithRoot(root, args)
				if err == nil {
					t.Fatalf("expected error for %s=%s, got nil", tc.flag, payload)
				}
				if !strings.Contains(err.Error(), "ERR_SCAFFOLD_INVALID_OPTS") &&
					!strings.Contains(err.Error(), "ERR_VALIDATION_FAILED") {
					t.Errorf("expected ERR_SCAFFOLD_INVALID_OPTS or ERR_VALIDATION_FAILED in error; got %v", err)
				}
			})
		}
	}
}

// TestRunScaffold_ControlCharInjection ensures owner team/role and journey
// goal/team free-text inputs reject newline / control characters that could
// inject YAML fields via the inline templates.
func TestRunScaffold_ControlCharInjection(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	cases := []struct {
		name string
		args []string
	}{
		// Free-text fields (validateScaffoldText path).
		{"journey_goal_newline", []string{"journey", "--id=mj", "--team=t", "--cells=examplecell", "--goal=evil\nextra: pwned"}},
		{"journey_team_newline", []string{"journey", "--id=mj", "--goal=g", "--cells=examplecell", "--team=evil\nextra: pwned"}},
		{"assembly_team_newline", []string{"assembly", "--id=as", "--cells=examplecell", "--role=r", "--team=evil\nextra: pwned"}},
		{"assembly_role_newline", []string{"assembly", "--id=as", "--cells=examplecell", "--team=t", "--role=evil\nextra: pwned"}},
		// Identifier fields (validateScaffoldID path) — must reject newlines too.
		// These fields reach inline YAML scalars (ownerCell, belongsToCell, etc.)
		// so newline injection would fabricate adjacent YAML keys without this guard.
		{"slice_id_newline", []string{"slice", "--cell=examplecell", "--id=evil\nextra: pwned"}},
		{"slice_cell_newline", []string{"slice", "--id=foo", "--cell=evil\nextra: pwned"}},
		{"contract_id_newline", []string{"contract", "--kind=http", "--owner=examplecell", "--id=evil\nextra: pwned"}},
		{"contract_owner_newline", []string{"contract", "--id=http.foo.bar.v1", "--kind=http", "--owner=evil\nextra: pwned"}},
		{"journey_id_newline", []string{"journey", "--goal=g", "--team=t", "--cells=examplecell", "--id=evil\nextra: pwned"}},
		{"journey_cells_newline", []string{"journey", "--id=mj", "--goal=g", "--team=t", "--cells=evil\nextra: pwned"}},
		{"assembly_id_newline", []string{"assembly", "--cells=examplecell", "--team=t", "--role=r", "--id=evil\nextra: pwned"}},
		{"assembly_cells_newline", []string{"assembly", "--id=as", "--team=t", "--role=r", "--cells=evil\nextra: pwned"}},
		// Round-7: close the scaffold-cell newline-injection gap (P0).
		// scaffoldCell was the only sub-command missing the validateScaffoldID /
		// validateScaffoldText guard at round-2; these cases lock it in.
		{"cell_id_newline", []string{"cell", "--team=t", "--role=r", "--id=evil\nextra: pwned"}},
		{"cell_team_newline", []string{"cell", "--id=ok", "--role=r", "--team=evil\nextra: pwned"}},
		{"cell_role_newline", []string{"cell", "--id=ok", "--team=t", "--role=evil\nextra: pwned"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := runScaffoldWithRoot(root, tc.args)
			if err == nil {
				t.Fatalf("expected error for control-char injection, got nil")
			}
		})
	}
}
