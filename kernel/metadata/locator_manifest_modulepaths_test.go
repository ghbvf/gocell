package metadata

import (
	"reflect"
	"testing"
	"testing/fstest"
)

// TestReadManifestModulePaths verifies the exported accessor returns the
// declared module disk paths in order and fails closed on a malformed manifest
// (it shares loadManifest's validation). It is the workspace enumerator's
// metadata-module source, cross-checked against go.work's `use` directives.
func TestReadManifestModulePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fsys    fstest.MapFS
		want    []string
		wantErr bool
	}{
		{
			name: "single module dot",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml":    &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n")},
				"cells/platform/cell.yaml": &fstest.MapFile{Data: []byte("id: platform\n")},
			},
			want: []string{"."},
		},
		{
			name: "multi module declaration order preserved",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml":      &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n  - path: mdm\n")},
				"cells/platform/cell.yaml":   &fstest.MapFile{Data: []byte("id: platform\n")},
				"mdm/cells/winmdm/cell.yaml": &fstest.MapFile{Data: []byte("id: winmdm\n")},
			},
			want: []string{".", "mdm"},
		},
		{
			name: "missing manifest fails closed",
			fsys: fstest.MapFS{
				"cells/platform/cell.yaml": &fstest.MapFile{Data: []byte("id: platform\n")},
			},
			wantErr: true,
		},
		{
			name: "bad version fails closed",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v2\nmodules:\n  - path: .\n")},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadManifestModulePaths(tt.fsys, DefaultManifestPath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadManifestModulePaths = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadManifestModulePaths unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ReadManifestModulePaths = %v, want %v", got, tt.want)
			}
		})
	}
}
