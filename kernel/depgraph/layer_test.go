package depgraph

import "testing"

const testModule = "github.com/ghbvf/gocell"

func TestLayerOf(t *testing.T) {
	t.Parallel()
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
			got := LayerOf(testModule, tt.path)
			if got != tt.want {
				t.Errorf("LayerOf(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestCellOf(t *testing.T) {
	t.Parallel()
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
			got := CellOf(testModule, tt.path)
			if got != tt.want {
				t.Errorf("CellOf(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestSliceOf(t *testing.T) {
	t.Parallel()
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
			got := SliceOf(testModule, tt.path)
			if got != tt.want {
				t.Errorf("SliceOf(%q) = %q, want %q", tt.path, got, tt.want)
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

// TestLayerOf_InternalUnknownDistinct locks the contract that
// module-internal-but-unmapped paths are distinguishable from true
// third-party paths. Collapsing both into LayerThirdParty is fail-open
// for governance: a new top-level directory under the module would look
// external and skip any layer rule that filters on module membership.
func TestLayerOf_InternalUnknownDistinct(t *testing.T) {
	t.Parallel()
	internalUnmapped := LayerOf(testModule, testModule+"/jobs/scheduler")
	externalThird := LayerOf(testModule, "github.com/jackc/pgx/v5")
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
