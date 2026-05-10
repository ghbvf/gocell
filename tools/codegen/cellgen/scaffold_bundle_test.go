package cellgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScaffoldCellBundle_HTTP is a RED test for K#09 cellgen.ScaffoldCellBundle:
// produces cell + 1 example slice + 1 HTTP contract bundle in one shot.
//
// Bundle output (relative to root):
//
//	cells/{id}/cell.yaml
//	cells/{id}/cell.go
//	cells/{id}/slices/{id}example/slice.yaml
//	cells/{id}/slices/{id}example/service.go
//	cells/{id}/slices/{id}example/service_test.go
//	contracts/http/{id}/example/v1/contract.yaml
//	contracts/http/{id}/example/v1/request.schema.json
//	contracts/http/{id}/example/v1/response.schema.json
func TestScaffoldCellBundle_HTTP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "myhttpcell",
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}

	// Verify bundle file inventory.
	wantFiles := []string{
		"cells/myhttpcell/cell.yaml",
		"cells/myhttpcell/cell.go",
		"cells/myhttpcell/slices/myhttpcellexample/slice.yaml",
		"cells/myhttpcell/slices/myhttpcellexample/service.go",
		"cells/myhttpcell/slices/myhttpcellexample/service_test.go",
		"contracts/http/myhttpcell/example/v1/contract.yaml",
		"contracts/http/myhttpcell/example/v1/request.schema.json",
		"contracts/http/myhttpcell/example/v1/response.schema.json",
	}
	for _, rel := range wantFiles {
		full := filepath.Join(dir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("bundle missing %s: %v", rel, err)
		}
	}

	// Verify contract.yaml does NOT carry an explicit `codegen:` line —
	// K#09 funnel: parser defaults Codegen to true so the field is redundant.
	// INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL
	cYAMLPath := filepath.Join(dir, "contracts", "http", "myhttpcell", "example", "v1", "contract.yaml")
	contractYAML, err := os.ReadFile(cYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read contract.yaml: %v", err)
	}
	if strings.Contains(string(contractYAML), "codegen:") {
		t.Errorf("scaffold contract.yaml must not declare codegen field (parser defaults to true); got:\n%s",
			string(contractYAML))
	}
}

// TestScaffoldCellBundle_Events is a RED test for the --with-events variant:
// produces an event contract with payload+headers schemas.
func TestScaffoldCellBundle_Events(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "myevtcell",
		StructName:       "MyEvtCell",
		Package:          "myevtcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithEvents:       true,
	}

	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}

	wantFiles := []string{
		"cells/myevtcell/cell.yaml",
		"cells/myevtcell/cell.go",
		"cells/myevtcell/slices/myevtcellexample/slice.yaml",
		"cells/myevtcell/slices/myevtcellexample/service.go",
		"cells/myevtcell/slices/myevtcellexample/service_test.go",
		"contracts/event/myevtcell/example/v1/contract.yaml",
		"contracts/event/myevtcell/example/v1/payload.schema.json",
		"contracts/event/myevtcell/example/v1/headers.schema.json",
	}
	for _, rel := range wantFiles {
		full := filepath.Join(dir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("event bundle missing %s: %v", rel, err)
		}
	}
}

// TestScaffoldCellBundle_DryRun verifies dry-run produces no files.
func TestScaffoldCellBundle_DryRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "drycell",
		StructName:       "DryCell",
		Package:          "drycell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
		DryRun:           true,
	}
	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("dry-run ScaffoldCellBundle: %v", err)
	}
	// In dry-run, the cell directory must not exist.
	if _, err := os.Stat(filepath.Join(dir, "cells", "drycell")); err == nil {
		t.Errorf("dry-run scaffold wrote files to disk")
	}
}

// TestScaffoldCellBundle_WithBoth verifies that --with-both produces both an HTTP
// slice (sliceID={id}example) and a separate event slice (sliceID={id}eventexample),
// each with their own contractUsages entry, so gocell validate ADV-06 passes.
func TestScaffoldCellBundle_WithBoth(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "mybothcell",
		StructName:       "MyBothCell",
		Package:          "mybothcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithBoth:         true,
	}

	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle WithBoth: %v", err)
	}

	// HTTP slice and contract must exist.
	httpFiles := []string{
		"cells/mybothcell/slices/mybothcellexample/slice.yaml",
		"cells/mybothcell/slices/mybothcellexample/service.go",
		"cells/mybothcell/slices/mybothcellexample/service_test.go",
		"contracts/http/mybothcell/example/v1/contract.yaml",
		"contracts/http/mybothcell/example/v1/request.schema.json",
		"contracts/http/mybothcell/example/v1/response.schema.json",
	}
	for _, rel := range httpFiles {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("WithBoth: missing HTTP file %s: %v", rel, err)
		}
	}

	// Event slice (separate sliceID) and contract must also exist.
	eventFiles := []string{
		"cells/mybothcell/slices/mybothcelleventexample/slice.yaml",
		"cells/mybothcell/slices/mybothcelleventexample/service.go",
		"cells/mybothcell/slices/mybothcelleventexample/service_test.go",
		"contracts/event/mybothcell/example/v1/contract.yaml",
		"contracts/event/mybothcell/example/v1/payload.schema.json",
		"contracts/event/mybothcell/example/v1/headers.schema.json",
	}
	for _, rel := range eventFiles {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("WithBoth: missing event file %s: %v", rel, err)
		}
	}

	// HTTP slice.yaml must reference the HTTP contract.
	httpSliceYAMLPath := filepath.Join(dir, "cells", "mybothcell", "slices", "mybothcellexample", "slice.yaml")
	httpSliceYAML, err := os.ReadFile(httpSliceYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read HTTP slice.yaml: %v", err)
	}
	if !strings.Contains(string(httpSliceYAML), "http.mybothcell.example.v1") {
		t.Errorf("HTTP slice.yaml must reference http.mybothcell.example.v1; got:\n%s", httpSliceYAML)
	}

	// Event slice.yaml must reference the event contract.
	evtSliceYAMLPath := filepath.Join(dir, "cells", "mybothcell", "slices", "mybothcelleventexample", "slice.yaml")
	evtSliceYAML, err := os.ReadFile(evtSliceYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read event slice.yaml: %v", err)
	}
	if !strings.Contains(string(evtSliceYAML), "event.mybothcell.example.v1") {
		t.Errorf("event slice.yaml must reference event.mybothcell.example.v1; got:\n%s", evtSliceYAML)
	}
}

// ---------------------------------------------------------------------------
// Symlink escape + atomic rollback tests (RED — 实现漏斗化后转 GREEN)
// ---------------------------------------------------------------------------

// TestScaffoldCellBundle_SymlinkEscape_Slice 验证 ScaffoldCellBundle 拒绝
// slices 目录是 root 外 symlink 的情况，且 outside 目录不被写入。
func TestScaffoldCellBundle_SymlinkEscape_Slice(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := t.TempDir()
	outside := t.TempDir()

	// 预创建 cells/myhttpcell，并将 slices 目录指向 outside
	cellDir := filepath.Join(root, "cells", "myhttpcell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	slicesLink := filepath.Join(cellDir, "slices")
	if err := os.Symlink(outside, slicesLink); err != nil {
		t.Fatalf("Symlink slices → outside: %v", err)
	}

	spec := ScaffoldSpec{
		CellID:           "myhttpcell",
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := ScaffoldCellBundle(root, spec)
	if err == nil {
		t.Fatal("ScaffoldCellBundle(slices symlink escape): want error, got nil")
	}
	// 错误消息应包含逃逸相关描述
	msg := err.Error()
	if !strings.ContainsAny(msg, "outside root,escapes,containment") &&
		!strings.Contains(msg, "outside") && !strings.Contains(msg, "escapes") &&
		!strings.Contains(msg, "containment") {
		t.Errorf("error must mention escape/containment; got: %v", err)
	}

	// outside 内不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("symlink escape: outside dir must be clean, got %v", entries)
	}
}

// TestScaffoldCellBundle_SymlinkEscape_Contract 验证 ScaffoldCellBundle 拒绝
// contracts 目录是 root 外 symlink 的情况。
func TestScaffoldCellBundle_SymlinkEscape_Contract(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := t.TempDir()
	outside := t.TempDir()

	// 预创建 contracts/http/myhttpcell → outside symlink
	contractParent := filepath.Join(root, "contracts", "http")
	if err := os.MkdirAll(contractParent, 0o755); err != nil {
		t.Fatal(err)
	}
	contractLink := filepath.Join(contractParent, "myhttpcell")
	if err := os.Symlink(outside, contractLink); err != nil {
		t.Fatalf("Symlink contract → outside: %v", err)
	}

	spec := ScaffoldSpec{
		CellID:           "myhttpcell",
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := ScaffoldCellBundle(root, spec)
	if err == nil {
		t.Fatal("ScaffoldCellBundle(contract symlink escape): want error, got nil")
	}

	// outside 内不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("contract symlink escape: outside dir must be clean, got %v", entries)
	}
}

// TestScaffoldCellBundle_AtomicRollback_OnContractConflict 验证：
// 预置 contract.yaml 冲突时，bundle 整体失败且 cells/myhttpcell 内无文件。
func TestScaffoldCellBundle_AtomicRollback_OnContractConflict(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	// 预置冲突 contract.yaml
	contractPath := filepath.Join(root, "contracts", "http", "myhttpcell", "example", "v1", "contract.yaml")
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, []byte("id: existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := ScaffoldSpec{
		CellID:           "myhttpcell",
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := ScaffoldCellBundle(root, spec)
	if err == nil {
		t.Fatal("ScaffoldCellBundle(contract conflict): want error, got nil")
	}

	// atomic：cells/myhttpcell/cell.yaml、cell.go、slices/.../slice.yaml 全不存在
	atomicAbsent := []string{
		"cells/myhttpcell/cell.yaml",
		"cells/myhttpcell/cell.go",
		"cells/myhttpcell/slices/myhttpcellexample/slice.yaml",
	}
	for _, rel := range atomicAbsent {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err == nil {
			t.Errorf("atomic rollback: %s must not exist after conflict error", rel)
		}
	}
}

// TestScaffoldCellBundle_AtomicRollback_OnContainmentFail 验证：
// slices symlink 逃逸时，cell.yaml 和 cell.go 也未写入。
func TestScaffoldCellBundle_AtomicRollback_OnContainmentFail(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := t.TempDir()
	outside := t.TempDir()

	// slices → outside symlink
	cellDir := filepath.Join(root, "cells", "myhttpcell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cellDir, "slices")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	spec := ScaffoldSpec{
		CellID:           "myhttpcell",
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := ScaffoldCellBundle(root, spec)
	if err == nil {
		t.Fatal("ScaffoldCellBundle(containment fail): want error, got nil")
	}

	// atomic：cell.yaml 和 cell.go 均未写入
	for _, rel := range []string{"cells/myhttpcell/cell.yaml", "cells/myhttpcell/cell.go"} {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err == nil {
			t.Errorf("containment fail rollback: %s must not exist", rel)
		}
	}
}

// TestScaffoldCellBundle_RejectKebabCellID verifies that ScaffoldCellBundle
// rejects a kebab-case CellID ("test-cell") with an error mentioning "kebab"
// or "dash" rather than silently stripping the dash and writing "testcell".
//
// RED: current implementation silently strips dashes via strings.ReplaceAll in
// planCellBundle, so ScaffoldCellBundle("test-cell") writes cells/test-cell/
// but uses "testcell" as the Go package name — an inconsistency. The exported
// API should reject kebab up-front with a clear error.
func TestScaffoldCellBundle_RejectKebabCellID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "test-cell", // kebab: must be rejected
		StructName:       "TestCell",
		Package:          "testcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L1",
		WithHTTP:         true,
	}
	err := ScaffoldCellBundle(dir, spec)
	if err == nil {
		t.Fatal("ScaffoldCellBundle(kebab CellID): want error, got nil")
	}
	// Error must mention kebab or dash so the message is actionable.
	msg := err.Error()
	if !strings.Contains(msg, "kebab") && !strings.Contains(msg, "dash") &&
		!strings.Contains(msg, "-") {
		t.Errorf("error must mention kebab/dash; got: %v", err)
	}
	// No files must be written.
	if _, statErr := os.Stat(filepath.Join(dir, "cells", "test-cell")); statErr == nil {
		t.Error("cells/test-cell must not exist after rejection")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "cells", "testcell")); statErr == nil {
		t.Error("cells/testcell (silently stripped) must not exist after rejection")
	}
}

// TestScaffoldCellBundle_BundleDefaultIsHTTP verifies that when neither
// WithHTTP nor WithEvents is set, default is HTTP.
func TestScaffoldCellBundle_BundleDefaultIsHTTP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "defcell",
		StructName:       "DefCell",
		Package:          "defcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
	}
	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "contracts", "http", "defcell", "example", "v1", "contract.yaml")); err != nil {
		t.Errorf("default bundle should produce HTTP contract; got: %v", err)
	}
}
