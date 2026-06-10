package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/packages"
)

// TestDeriveCellImportPrefix pins the layout-agnostic sibling-cell import-prefix
// derivation (#1560 F9): the prefix is read off the cell's own loaded package
// path, so root cells/, the corecells flat module and examples/*/cells/ all
// resolve correctly without hardcoding one module path.
func TestDeriveCellImportPrefix(t *testing.T) {
	pkgs := func(paths ...string) []*packages.Package {
		out := make([]*packages.Package, len(paths))
		for i, p := range paths {
			out[i] = &packages.Package{PkgPath: p}
		}
		return out
	}
	tests := []struct {
		name        string
		pkgs        []*packages.Package
		cellDirName string
		want        string
	}{
		{
			name: "corecells flat module",
			pkgs: pkgs(
				"github.com/ghbvf/gocell/corecells/accesscore",
				"github.com/ghbvf/gocell/corecells/accesscore/slices/setup",
				"github.com/ghbvf/gocell/corecells/accesscore/internal/domain",
			),
			cellDirName: "accesscore",
			want:        "github.com/ghbvf/gocell/corecells/",
		},
		{
			name:        "root cells layout",
			pkgs:        pkgs("example.com/proj/cells/mycell", "example.com/proj/cells/mycell/slices/s"),
			cellDirName: "mycell",
			want:        "example.com/proj/cells/",
		},
		{
			name:        "example-local cells layout",
			pkgs:        pkgs("example.com/proj/examples/todoorder/cells/order", "example.com/proj/examples/todoorder/cells/order/internal/x"),
			cellDirName: "order",
			want:        "example.com/proj/examples/todoorder/cells/",
		},
		{
			name:        "subdir shares cell name picks the shortest (cell root)",
			pkgs:        pkgs("m/corecells/accesscore/internal/accesscore", "m/corecells/accesscore"),
			cellDirName: "accesscore",
			want:        "m/corecells/",
		},
		{
			name:        "underivable returns empty (no sibling detection)",
			pkgs:        pkgs("m/other/pkg"),
			cellDirName: "accesscore",
			want:        "",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, deriveCellImportPrefix(tc.pkgs, tc.cellDirName))
		})
	}
}
