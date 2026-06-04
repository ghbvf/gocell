package depgraph

import "testing"

const testModule = "github.com/ghbvf/gocell"

// singleModuleClassifier is the degenerate one-element set; its classification
// must be byte-identical to the former module-singular LayerOf/CellOf/SliceOf.
func singleModuleClassifier() Classifier {
	return NewClassifier([]string{testModule})
}

func TestClassifier_Layer(t *testing.T) {
	t.Parallel()
	c := singleModuleClassifier()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", ""},
		{"module_root", testModule, LayerRoot},
		{"kernel", testModule + "/kernel/cell", LayerKernel},
		{"kernel_nested", testModule + "/kernel/governance/depcheck", LayerKernel},
		{"runtime", testModule + "/runtime/http/middleware", LayerRuntime},
		{"adapters", testModule + "/adapters/postgres", LayerAdapters},
		{"cells", testModule + "/cells/accesscore", LayerCells},
		{"cells_slice", testModule + "/cells/accesscore/slices/sessionlogin", LayerCells},
		{"pkg", testModule + "/pkg/errcode", LayerPkg},
		{"cmd", testModule + "/cmd/gocell/app", LayerCmd},
		{"examples", testModule + "/examples/ssobff", LayerExamples},
		{"tools", testModule + "/tools/archtest", LayerTools},
		{"tests", testModule + "/tests/integration/foo", LayerTests},
		{"generated", testModule + "/generated/contracts/foo", LayerGenerated},
		{"unknown_internal_segment", testModule + "/oddbucket/foo", LayerUnknown},
		{"unknown_internal_single_segment", testModule + "/oddbucket", LayerUnknown},
		{"stdlib_short", "fmt", LayerStdlib},
		{"stdlib_nested", "encoding/json", LayerStdlib},
		{"stdlib_net", "net/http", LayerStdlib},
		{"thirdparty_github", "github.com/jackc/pgx/v5", LayerThirdParty},
		{"thirdparty_xtools", "golang.org/x/tools/go/packages", LayerThirdParty},
		{"thirdparty_other_module", "github.com/other/module", LayerThirdParty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Layer(tt.path)
			if got != tt.want {
				t.Errorf("Layer(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestClassifier_Cell(t *testing.T) {
	t.Parallel()
	c := singleModuleClassifier()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"non_cell_kernel", testModule + "/kernel/cell", ""},
		{"non_cell_root", testModule, ""},
		{"cell_root", testModule + "/cells/accesscore", "accesscore"},
		{"cell_internal", testModule + "/cells/accesscore/internal/domain", "accesscore"},
		{"cell_slice", testModule + "/cells/auditcore/slices/journal", "auditcore"},
		{"cells_dir_only", testModule + "/cells", ""},
		{"shared_internal_helper", testModule + "/cells/internal/testoutbox", ""},
		{"stdlib", "fmt", ""},
		{"thirdparty", "github.com/foo/bar", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Cell(tt.path)
			if got != tt.want {
				t.Errorf("Cell(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestClassifier_Slice(t *testing.T) {
	t.Parallel()
	c := singleModuleClassifier()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"slice_root", testModule + "/cells/accesscore/slices/sessionlogin", "sessionlogin"},
		{"slice_nested", testModule + "/cells/accesscore/slices/sessionlogin/handlers", "sessionlogin"},
		{"cell_root_no_slice", testModule + "/cells/accesscore", ""},
		{"cell_internal_no_slice", testModule + "/cells/accesscore/internal/domain", ""},
		{"non_cell", testModule + "/kernel/cell", ""},
		{"stdlib", "fmt", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Slice(tt.path)
			if got != tt.want {
				t.Errorf("Slice(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestClassifier_MultiModule is the core new-behavior assertion: a nested module
// whose path is a string subpath of the core module is classified within its
// OWN module (longest-prefix owner) — so .../mdm/cells/foo is LayerCells, not the
// LayerUnknown a single-prefix classifier would have produced.
func TestClassifier_MultiModule(t *testing.T) {
	t.Parallel()
	const (
		core      = "github.com/ghbvf/gocell"
		mdm       = "github.com/ghbvf/gocell/mdm"
		satellite = "example.test/satellite" // unrelated path (not a core subpath)
	)
	c := NewClassifier([]string{core, mdm, satellite})

	tests := []struct {
		name      string
		path      string
		wantOwner string
		wantLayer string
		wantCell  string
		wantSlice string
	}{
		{"core_kernel", core + "/kernel/cell", core, LayerKernel, "", ""},
		{"core_cell", core + "/cells/accesscore", core, LayerCells, "accesscore", ""},
		// Longest-prefix: mdm wins over core for an mdm-owned package.
		{"mdm_root", mdm, mdm, LayerRoot, "", ""},
		{"mdm_cell", mdm + "/cells/winmdm", mdm, LayerCells, "winmdm", ""},
		{"mdm_cell_slice", mdm + "/cells/winmdm/slices/enroll", mdm, LayerCells, "winmdm", "enroll"},
		{"mdm_kernel", mdm + "/kernel/foo", mdm, LayerKernel, "", ""},
		{"mdm_unknown_dir", mdm + "/oddbucket/x", mdm, LayerUnknown, "", ""},
		// Satellite with an unrelated module path (not a core subpath).
		{"satellite_cell", satellite + "/cells/sat", satellite, LayerCells, "sat", ""},
		// A package under no member module is third-party.
		{"foreign", "github.com/other/mod/pkg", "", LayerThirdParty, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.OwningModule(tt.path); got != tt.wantOwner {
				t.Errorf("OwningModule(%q) = %q, want %q", tt.path, got, tt.wantOwner)
			}
			if got := c.Layer(tt.path); got != tt.wantLayer {
				t.Errorf("Layer(%q) = %q, want %q", tt.path, got, tt.wantLayer)
			}
			if got := c.Cell(tt.path); got != tt.wantCell {
				t.Errorf("Cell(%q) = %q, want %q", tt.path, got, tt.wantCell)
			}
			if got := c.Slice(tt.path); got != tt.wantSlice {
				t.Errorf("Slice(%q) = %q, want %q", tt.path, got, tt.wantSlice)
			}
		})
	}
}

func TestIsStdlib(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{"", false},
		{"fmt", true},
		{"net/http", true},
		{"encoding/json", true},
		{"github.com/foo/bar", false},
		{"golang.org/x/tools", false},
		{"google.golang.org/grpc", false},
		{"go.uber.org/zap", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsStdlib(tt.path); got != tt.want {
				t.Errorf("IsStdlib(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestNewClassifier_EmptyStringFilter verifies that an empty-string entry in
// the modules slice is dropped by NewClassifier. An empty-prefix module would
// match every import path (strings.HasPrefix(x, "") is always true), causing
// fmt, encoding/json, and third-party paths to be "owned" by the empty module
// and mis-classified as LayerUnknown instead of LayerStdlib/LayerThirdParty.
func TestNewClassifier_EmptyStringFilter(t *testing.T) {
	t.Parallel()
	// Construct with an empty string alongside a real module path.
	c := NewClassifier([]string{"", "github.com/ghbvf/gocell"})
	// stdlib must still classify as LayerStdlib, not be "owned" by the empty module.
	if got := c.OwningModule("fmt"); got != "" {
		t.Errorf("OwningModule(\"fmt\") = %q, want \"\" (empty-prefix entry must be dropped)", got)
	}
	if got := c.Layer("fmt"); got != LayerStdlib {
		t.Errorf("Layer(\"fmt\") = %q, want %q (empty-prefix entry must be dropped)", got, LayerStdlib)
	}
	// The real module still works.
	if got := c.OwningModule("github.com/ghbvf/gocell/kernel/cell"); got != "github.com/ghbvf/gocell" {
		t.Errorf("OwningModule core pkg = %q, want core module", got)
	}
}

// TestClassifier_InternalUnknownDistinct locks the contract that
// module-internal-but-unmapped paths are distinguishable from true third-party
// paths. Collapsing both into LayerThirdParty is fail-open for governance: a new
// top-level directory under a module would look external and skip any layer rule
// that filters on module membership.
func TestClassifier_InternalUnknownDistinct(t *testing.T) {
	t.Parallel()
	c := singleModuleClassifier()
	internalUnmapped := c.Layer(testModule + "/jobs/scheduler")
	externalThird := c.Layer("github.com/jackc/pgx/v5")
	if internalUnmapped == externalThird {
		t.Fatalf("internal-unknown and third-party must be distinct; both got %q", internalUnmapped)
	}
	if internalUnmapped != LayerUnknown {
		t.Errorf("internal-unmapped path: got %q, want %q", internalUnmapped, LayerUnknown)
	}
	if externalThird != LayerThirdParty {
		t.Errorf("true third-party path: got %q, want %q", externalThird, LayerThirdParty)
	}
}

func TestIsThirdParty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"empty", "", false},
		{"module_root", testModule, false},
		{"module_subpkg", testModule + "/kernel", false},
		{"stdlib", "fmt", false},
		{"thirdparty", "github.com/jackc/pgx/v5", true},
		{"xtools", "golang.org/x/tools/go/packages", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsThirdParty(testModule, tt.path); got != tt.want {
				t.Errorf("IsThirdParty(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
