package markergen

import (
	"path/filepath"
	"runtime"
	"testing"
)

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func TestCollectFromCellFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		fixture       string
		wantCount     int
		wantNames     []string // expected marker names in order
		wantTargets   []markerTarget
		wantFieldName []string // "" for typeLevel
	}{
		{
			name:      "happy path: two listeners + one route + one subscribe",
			fixture:   "cell_happy.go",
			wantCount: 4,
			wantNames: []string{
				"cell:listener",
				"cell:listener",
				"slice:route",
				"slice:subscribe",
			},
			wantTargets: []markerTarget{
				typeLevel,
				typeLevel,
				fieldLevel,
				fieldLevel,
			},
			wantFieldName: []string{"", "", "CreateHandler", "ConfigSub"},
		},
		{
			name:      "no markers",
			fixture:   "cell_empty.go",
			wantCount: 0,
		},
		{
			name:      "non-gocell markers ignored",
			fixture:   "cell_othertools.go",
			wantCount: 0,
		},
		{
			name:          "cell_withmarkers: fieldLevel FieldName extraction",
			fixture:       "cell_withmarkers.go",
			wantCount:     3,
			wantNames:     []string{"cell:listener", "slice:route", "slice:subscribe"},
			wantTargets:   []markerTarget{typeLevel, fieldLevel, fieldLevel},
			wantFieldName: []string{"", "Items", "Sub"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := testdataPath(t, tc.fixture)
			got, err := CollectFromCellFile(path)
			if err != nil {
				t.Fatalf("CollectFromCellFile: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d markers, want %d; markers=%v", len(got), tc.wantCount, got)
			}
			for i, m := range got {
				if m.Name != tc.wantNames[i] {
					t.Errorf("[%d] name=%q, want %q", i, m.Name, tc.wantNames[i])
				}
				if m.Target != tc.wantTargets[i] {
					t.Errorf("[%d] target=%v, want %v", i, m.Target, tc.wantTargets[i])
				}
				if m.FieldName != tc.wantFieldName[i] {
					t.Errorf("[%d] fieldName=%q, want %q", i, m.FieldName, tc.wantFieldName[i])
				}
				if m.Line <= 0 {
					t.Errorf("[%d] line should be > 0, got %d", i, m.Line)
				}
			}
		})
	}
}

func TestCollectFromCellFile_NotFound(t *testing.T) {
	t.Parallel()
	_, err := CollectFromCellFile("/nonexistent/path/cell.go")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCollectFromCellFile_KVPreserved(t *testing.T) {
	t.Parallel()
	path := testdataPath(t, "cell_happy.go")
	got, err := CollectFromCellFile(path)
	if err != nil {
		t.Fatalf("CollectFromCellFile: %v", err)
	}
	// First marker: cell:listener with ref+prefix
	if got[0].KVLine != "ref=cell.PrimaryListener,prefix=/api/v1" {
		t.Errorf("kv[0]=%q", got[0].KVLine)
	}
	// Third marker: slice:route
	if got[2].KVLine != "slice=ordercreate,subPath=/orders" {
		t.Errorf("kv[2]=%q", got[2].KVLine)
	}
}
