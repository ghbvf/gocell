package assembly

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestPlanAssemblyScaffold_ExplicitSameModuleNotCrossModule verifies that a
// ScaffoldCellRef whose Module equals the assembly's own module is treated as
// same-module: derived files are NOT skipped and compositionAPI is not emitted.
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
	assert.NotContains(t, planContent(t, plan, "assembly.yaml"), "compositionAPI",
		"module == own module must not trigger compositionAPI")
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
