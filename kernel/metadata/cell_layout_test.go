package metadata

import "testing"

func TestCellLocationFromRel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		rel      string
		wantID   string
		wantKind CellRootKind
		wantRoot string
		wantOK   bool
	}{
		{"cells/order/cell.yaml", "order", CellRootLocal, "cells/order", true},
		{"cells/order/slices/create/service.go", "order", CellRootLocal, "cells/order", true},
		{"corecells/accesscore/cell.yaml", "accesscore", CellRootPlatform, "corecells/accesscore", true},
		{"corecells/accesscore/slices/sessionlogin/service.go", "accesscore", CellRootPlatform, "corecells/accesscore", true},
		{"examples/demo/cells/democell/cell.yaml", "democell", CellRootExample, "examples/demo/cells/democell", true},
		{"examples/demo/cells/democell/slices/hello/service.go", "democell", CellRootExample, "examples/demo/cells/democell", true},
		{"kernel/cell/cell.go", "", "", "", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.rel, func(t *testing.T) {
			t.Parallel()

			got, ok := CellLocationFromRel(tc.rel)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.ID != tc.wantID || got.Kind != tc.wantKind || got.RootRel != tc.wantRoot {
				t.Fatalf("CellLocationFromRel(%q) = %+v", tc.rel, got)
			}
		})
	}
}

func TestCellDirFromMetadataFile(t *testing.T) {
	t.Parallel()

	got, ok := CellDirFromMetadataFile("corecells/accesscore/cell.yaml")
	if !ok || got != "corecells/accesscore" {
		t.Fatalf("CellDirFromMetadataFile corecells = (%q,%v)", got, ok)
	}

	got, ok = CellDirFromMetadataFile("cells/order/slices/create/slice.yaml")
	if ok || got != "" {
		t.Fatalf("CellDirFromMetadataFile slice = (%q,%v), want empty false", got, ok)
	}
}

func TestCellIDFromImportPath(t *testing.T) {
	t.Parallel()

	const mod = "github.com/ghbvf/gocell"
	cases := []struct {
		importPath string
		want       string
		wantOK     bool
	}{
		{mod + "/cells/order/slices/create", "order", true},
		{mod + "/corecells/accesscore/slices/sessionlogin", "accesscore", true},
		{mod + "/examples/demo/cells/democell/slices/hello", "democell", true},
		{mod + "/runtime/auth", "", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.importPath, func(t *testing.T) {
			t.Parallel()

			got, ok := CellIDFromImportPath(mod, tc.importPath)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("CellIDFromImportPath = (%q,%v), want (%q,%v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
