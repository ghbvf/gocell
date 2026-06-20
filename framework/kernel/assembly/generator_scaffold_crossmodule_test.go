package assembly

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/pathsafe"
)

const scaffoldCrossModule = "github.com/ghbvf/gocell-mdm"

// errMessage extracts the const Message of an *errcode.Error (Error() renders
// internal details over the message, so message-text assertions need the field).
func errMessage(err error) string {
	var ce *errcode.Error
	if errors.As(err, &ce) {
		return ce.Message
	}
	return err.Error()
}

// planContent returns the rendered content of the planned file whose path ends
// with suffix, or fails the test.
func planContent(t *testing.T, plan []pathsafe.PlannedFile, suffix string) string {
	t.Helper()
	for _, f := range plan {
		if strings.HasSuffix(f.AbsPath, suffix) {
			return string(f.Content)
		}
	}
	t.Fatalf("planned file ending %q not found in %d files", suffix, len(plan))
	return ""
}

// TestPlanAssemblyScaffold_CrossModule_SkeletonOnly verifies that a spec mixing
// a same-module and a cross-module cell renders only the 3 skeleton files
// (cross-module implicitly skips the K#10 derived files) and that the
// assembly.yaml carries the object form + build.compositionAPI: true.
func TestPlanAssemblyScaffold_CrossModule_SkeletonOnly(t *testing.T) {
	t.Parallel()
	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID: mustID(t, "mdm"),
		Cells: []ScaffoldCellRef{
			{ID: mustID(t, "examplecell")},
			{ID: mustID(t, "enrollcell"), Module: scaffoldCrossModule},
		},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	require.NoError(t, err)
	require.Len(t, plan, 3, "cross-module scaffold must emit skeleton only (assembly.yaml + run.go + app.go)")

	asm := planContent(t, plan, "assembly.yaml")
	assert.Contains(t, asm, "- examplecell", "same-module cell keeps scalar shorthand")
	assert.Contains(t, asm, "- {id: enrollcell, module: "+scaffoldCrossModule+"}", "cross-module cell renders object form")
	assert.Contains(t, asm, "compositionAPI: true", "cross-module assembly must declare compositionAPI")
}

// TestPlanAssemblyScaffold_CrossModule_RunGoCompositionContract pins F1: a
// cross-module (build.compositionAPI: true) scaffold must emit a run.go whose
// module helper consumes []composition.CellModule — the SAME type the
// composition modules_gen.go returns from generatedCellModules() (asserted by
// TestGenerateModulesGen_CompositionForm). The legacy run.go skeleton declared a
// local `CellModule` interface + stub *Module types and consumed []CellModule,
// which can never compile against the composition modules_gen.go's
// `func generatedCellModules() []composition.CellModule` — breaking the
// scaffold→generate→compile loop for cross-module assemblies. run.go and
// modules_gen.go share `package main`, so the two generatedCellModules() shapes
// must agree.
func TestPlanAssemblyScaffold_CrossModule_RunGoCompositionContract(t *testing.T) {
	t.Parallel()
	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID: mustID(t, "mdm"),
		Cells: []ScaffoldCellRef{
			{ID: mustID(t, "examplecell")},
			{ID: mustID(t, "enrollcell"), Module: scaffoldCrossModule},
		},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	require.NoError(t, err)

	runGo := planContent(t, plan, "run.go")

	// Composition-API contract: helper consumes []composition.CellModule and
	// imports runtime/composition — matching the modules_gen.go `generate
	// assembly` emits for build.compositionAPI: true.
	assert.Contains(t, runGo, "[]composition.CellModule",
		"cross-module run.go helper must consume []composition.CellModule")
	assert.Contains(t, runGo, `"github.com/ghbvf/gocell/framework/runtime/composition"`,
		"cross-module run.go must import runtime/composition")
	// Must NOT carry the legacy local-type contract that collides with the
	// composition modules_gen.go's generatedCellModules() []composition.CellModule.
	assert.NotContains(t, runGo, "type CellModule interface",
		"cross-module run.go must not redeclare a local CellModule type")
	assert.NotContains(t, runGo, "Module struct{}",
		"cross-module run.go must not declare stub *Module types")
	assert.NotContains(t, runGo, "[]CellModule",
		"cross-module run.go must not consume the legacy local []CellModule")
}

// TestPlanAssemblyScaffold_ExplicitSameModuleNotCrossModule verifies that a
// ScaffoldCellRef whose Module equals the assembly's own module is treated as
// same-module: derived files are NOT skipped, compositionAPI is not emitted, and
// the redundant `module:` is normalized away to the scalar shorthand.
func TestPlanAssemblyScaffold_ExplicitSameModuleNotCrossModule(t *testing.T) {
	t.Parallel()
	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID:        mustID(t, "samemod"),
		Cells:     []ScaffoldCellRef{{ID: mustID(t, "examplecell"), Module: "github.com/ghbvf/gocell"}},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	require.NoError(t, err)
	require.Len(t, plan, 6, "explicit same-module ref must keep the full derived plan")
	asm := planContent(t, plan, "assembly.yaml")
	assert.NotContains(t, asm, "compositionAPI", "module == own module must not trigger compositionAPI")
	assert.NotContains(t, asm, "module:", "module == own module must render scalar shorthand, no redundant module:")
	assert.Contains(t, asm, "- examplecell", "must render the same-module scalar form")
}

// TestScaffoldAssembly_CrossModuleYAMLInjection asserts that a cross-module
// cell's module path — rendered into the flow-mapping `- {id: X, module: Y}`
// form — cannot break out of the scalar to inject adjacent keys or extra cells.
// MatchAssemblyModulePath rejects control/space/quote/backtick/backslash upstream;
// the remaining YAML-significant runes ({ } [ ] : , ' # & * ! | > %) are handled
// by yamlsafe.Quote. This is the cross-module counterpart of
// TestScaffoldAssembly_YAMLScalarInjection (which covers owner.team/role).
func TestScaffoldAssembly_CrossModuleYAMLInjection(t *testing.T) {
	t.Parallel()
	// Each module string passes MatchAssemblyModulePath but carries a YAML
	// metacharacter class that would break the flow-mapping without quoting.
	modules := []struct {
		name   string
		module string
	}{
		{"brace_close", "github.com/acme/x}injected"},
		{"colon_comma", "github.com/acme/x,injected:true"},
		{"single_quote", "github.com/acme/x'injected"},
		{"flow_seq", "github.com/acme/[x]injected"},
		{"yaml_indicators", "github.com/acme/x#&*!|>%"},
	}
	for _, tc := range modules {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, pm := scaffoldTestProject(t)
			gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)
			spec := AssemblyScaffoldSpec{
				ID:        mustID(t, "mdm"),
				Cells:     []ScaffoldCellRef{{ID: mustID(t, "enrollcell"), Module: tc.module}},
				OwnerTeam: "platform",
				OwnerRole: "maintainer",
			}
			plan, err := gen.PlanAssemblyScaffold(spec)
			require.NoError(t, err)
			asmYAML := []byte(planContent(t, plan, "assembly.yaml"))

			// Top-level / build keys: no injected adjacent keys (reuses the
			// owner-injection structural guard).
			assertNoInjectedAdjacentKeys(t, asmYAML)

			// cells round-trips to exactly one {id, module} entry, module verbatim,
			// no smuggled extra cell or key.
			var doc struct {
				Cells []map[string]any `yaml:"cells"`
			}
			require.NoError(t, yaml.Unmarshal(asmYAML, &doc), "rendered yaml must parse")
			require.Len(t, doc.Cells, 1, "exactly one cell — no injected entry")
			assert.Equal(t, "enrollcell", doc.Cells[0]["id"])
			assert.Equal(t, tc.module, doc.Cells[0]["module"], "module must round-trip verbatim")
			assert.Len(t, doc.Cells[0], 2, "cell entry must carry only id + module")
		})
	}
}

// TestValidateScaffoldCells covers existence (same-module), existence-skip
// (cross-module), duplicate rejection, and module-path hygiene.
func TestValidateScaffoldCells(t *testing.T) {
	t.Parallel()
	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	cases := []struct {
		name    string
		cells   []ScaffoldCellRef
		wantErr string // substring; "" = expect success
	}{
		{
			name:  "same_module_exists",
			cells: []ScaffoldCellRef{{ID: mustID(t, "examplecell")}},
		},
		{
			name:    "same_module_unknown",
			cells:   []ScaffoldCellRef{{ID: mustID(t, "ghostcell")}},
			wantErr: "unknown cell",
		},
		{
			name:  "cross_module_skips_existence",
			cells: []ScaffoldCellRef{{ID: mustID(t, "ghostcell"), Module: scaffoldCrossModule}},
		},
		{
			name: "duplicate_cell",
			cells: []ScaffoldCellRef{
				{ID: mustID(t, "examplecell")},
				{ID: mustID(t, "examplecell")},
			},
			wantErr: "duplicate cell",
		},
		{
			name:    "cross_module_bad_path",
			cells:   []ScaffoldCellRef{{ID: mustID(t, "examplecell"), Module: "github.com/acme/x y"}},
			wantErr: "invalid character",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := gen.validateScaffoldCells(tc.cells)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, errMessage(err), tc.wantErr)
		})
	}
}
