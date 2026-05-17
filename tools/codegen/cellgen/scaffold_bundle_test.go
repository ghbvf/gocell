package cellgen

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/scaffoldid"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// scaffoldBundleSkip is a test helper that calls PlanCellBundleScaffold
// (with SkipGenerate forced true so no metadata parse is needed) and then
// WritePlannedFiles(dryRun=false) to write skeleton files. Returns the first
// error encountered (plan or write).
func scaffoldBundleSkip(t *testing.T, root string, spec ScaffoldSpec) error {
	t.Helper()
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return err
	}
	spec.SkipGenerate = true
	plan, err := PlanCellBundleScaffold(realRoot, spec)
	if err != nil {
		return err
	}
	return pathsafe.WritePlannedFiles(realRoot, mustPlanSet(t, plan), false)
}

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
		CellID:           scaffoldid.MustParse("myhttpcell"),
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip: %v", err)
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
		CellID:           scaffoldid.MustParse("myevtcell"),
		StructName:       "MyEvtCell",
		Package:          "myevtcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithEvents:       true,
	}

	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip: %v", err)
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

// TestScaffoldCellBundle_DryRun verifies that PlanCellBundleScaffold (skeleton only)
// followed by WritePlannedFiles(dryRun=true) produces no files.
func TestScaffoldCellBundle_DryRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("drycell"),
		StructName:       "DryCell",
		Package:          "drycell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
		SkipGenerate:     true, // skip codegen stage so no metadata parse needed
	}
	realRoot, err := pathsafe.ResolveRoot(dir)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	plan, err := PlanCellBundleScaffold(realRoot, spec)
	if err != nil {
		t.Fatalf("PlanCellBundleScaffold: %v", err)
	}
	if err := pathsafe.WritePlannedFiles(realRoot, mustPlanSet(t, plan), true); err != nil {
		t.Fatalf("WritePlannedFiles dry-run: %v", err)
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
		CellID:           scaffoldid.MustParse("mybothcell"),
		StructName:       "MyBothCell",
		Package:          "mybothcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithBoth:         true,
	}

	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip WithBoth: %v", err)
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
		CellID:           scaffoldid.MustParse("myhttpcell"),
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := scaffoldBundleSkip(t, root, spec)
	if err == nil {
		t.Fatal("scaffoldBundleSkip(slices symlink escape): want error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error for symlink escape, got %T: %v", err, err)
	}
	// pathsafe wraps symlink-escapes as KindInvalid/ErrValidationFailed at the
	// leaf check, but the scaffold bundle layer may re-wrap as ErrInternal.
	// We verify the error is a structured *errcode.Error (not a raw error) and
	// that the Kind signals a rejection (not a success), which is sufficient for
	// this containment boundary test.
	if ec.Code != errcode.ErrValidationFailed && ec.Code != errcode.ErrInternal {
		t.Errorf("expected ErrValidationFailed or ErrInternal for symlink escape, got %q", ec.Code)
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
		CellID:           scaffoldid.MustParse("myhttpcell"),
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := scaffoldBundleSkip(t, root, spec)
	if err == nil {
		t.Fatal("scaffoldBundleSkip(contract symlink escape): want error, got nil")
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
		CellID:           scaffoldid.MustParse("myhttpcell"),
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := scaffoldBundleSkip(t, root, spec)
	if err == nil {
		t.Fatal("scaffoldBundleSkip(contract conflict): want error, got nil")
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
		CellID:           scaffoldid.MustParse("myhttpcell"),
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	err := scaffoldBundleSkip(t, root, spec)
	if err == nil {
		t.Fatal("scaffoldBundleSkip(containment fail): want error, got nil")
	}

	// atomic：cell.yaml 和 cell.go 均未写入
	for _, rel := range []string{"cells/myhttpcell/cell.yaml", "cells/myhttpcell/cell.go"} {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err == nil {
			t.Errorf("containment fail rollback: %s must not exist", rel)
		}
	}
}

// Kebab-CellID rejection coverage moved upstream:
// kernel/scaffoldid_test.go.TestParse_Reject `dash` case asserts
// scaffoldid.Parse("test-cell") returns ErrValidationFailed, and
// ScaffoldSpec.CellID is typed (scaffoldid.ScaffoldID) so a kebab string
// cannot reach the cellgen funnel without first failing at the cmd-flag
// boundary's scaffoldid.Parse call (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01).
// The previous ScaffoldCellBundle-level reject test is no longer applicable.

// TestScaffoldCellBundle_BundleDefaultIsHTTP verifies that when neither
// WithHTTP nor WithEvents is set, default is HTTP.
func TestScaffoldCellBundle_BundleDefaultIsHTTP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("defcell"),
		StructName:       "DefCell",
		Package:          "defcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
	}
	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "contracts", "http", "defcell", "example", "v1", "contract.yaml")); err != nil {
		t.Errorf("default bundle should produce HTTP contract; got: %v", err)
	}
}

// TestPlanBundleFiles_ErrorCarriesKindLabelInDetails asserts that when the
// shared slice/contract render pipeline (planBundleFiles) fails at the
// ContainPath leaf, the wrapped *errcode.Error carries the caller-supplied
// kindLabel as a `kind` detail. This locks the contract that bundle-layer
// errors disambiguate slice vs contract origin through structured details
// rather than message-string prefixes — protecting against re-introduction
// of message/kind coupling (PR453 P2 finding).
func TestPlanBundleFiles_ErrorCarriesKindLabelInDetails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	cases := []struct {
		name      string
		kindLabel string
	}{
		{"slice path", "slice"},
		{"contract path", "contract"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Absolute targetRel forces pathsafe.ContainPath to reject before
			// any template work, exercising the planBundleFiles top-level wrap.
			_, err := planBundleFiles(root, "/escapes", nil, nil, nil, tc.kindLabel)
			if err == nil {
				t.Fatal("planBundleFiles with absolute targetRel: want error, got nil")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("err is not *errcode.Error: %T (%v)", err, err)
			}
			attr, ok := ec.FindAttr("kind")
			if !ok {
				t.Fatalf("planBundleFiles wrap must carry 'kind' detail; got details=%v", ec.Details)
			}
			if got := attr.Value.String(); got != tc.kindLabel {
				t.Errorf("kind detail = %q, want %q", got, tc.kindLabel)
			}
			// Message must remain neutral (no slice/contract leakage) so that
			// kind disambiguation lives in structured details only.
			if strings.Contains(ec.Message, tc.kindLabel) {
				t.Errorf("message %q must not embed kind label %q; carry it in details instead",
					ec.Message, tc.kindLabel)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P3: ScaffoldSpec.Validate — consistency level vs bundle variants (Wave 5)
// ---------------------------------------------------------------------------

// TestScaffoldSpec_Validate_RejectsL1WithEvents asserts that specifying
// consistencyLevel=L1 with an event-publishing variant (withEvents=true)
// returns a validation error — event slices require at least L2 (OutboxFact).
func TestScaffoldSpec_Validate_RejectsL1WithEvents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("badevtcell"),
		StructName:       "BadEvtCell",
		Package:          "badevtcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L1",
		WithEvents:       true,
	}
	err := scaffoldBundleSkip(t, dir, spec)
	if err == nil {
		t.Fatal("expected validation error for L1+WithEvents, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
}

// TestScaffoldSpec_Validate_AcceptsL2WithEvents asserts that L2+withEvents
// passes validation without error.
func TestScaffoldSpec_Validate_AcceptsL2WithEvents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("goodevtcell"),
		StructName:       "GoodEvtCell",
		Package:          "goodevtcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithEvents:       true,
	}
	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("expected no error for L2+WithEvents, got: %v", err)
	}
}

// TestPlanEventExampleArtifacts_HTTPAndEvents_DistinctSliceID is a RED test for
// SCAFFOLD-BUNDLE-VARIANT-DUPLICATE-PATH-01. When WithHTTP=true and
// WithEvents=true (WithBoth=false), the event slice must use a distinct sliceID
// (cellNoDash+"eventexample") instead of the same sliceID as the HTTP slice.
// The old code only assigned the distinct ID when WithBoth=true, causing both
// HTTP and event slices to write under the same directory → duplicate AbsPath.
func TestPlanEventExampleArtifacts_HTTPAndEvents_DistinctSliceID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("myhttpcell"),
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2", // events require >= L2
		WithHTTP:         true,
		WithEvents:       true,
		// WithBoth intentionally false — this is the degenerate "both flags" path
	}

	realRoot, err := pathsafe.ResolveRoot(dir)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	plan, err := planCellBundle(realRoot, spec)
	if err != nil {
		t.Fatalf("planCellBundle: %v", err)
	}

	// Assert no duplicate AbsPath entries.
	seen := make(map[string]int)
	for _, f := range plan {
		seen[f.AbsPath]++
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("duplicate AbsPath in plan: %s (count=%d)", path, count)
		}
	}

	// Assert that the HTTP and event slices have distinct directories.
	var httpSliceDir, eventSliceDir string
	for _, f := range plan {
		rel, _ := filepath.Rel(realRoot, f.AbsPath)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "cells/myhttpcell/slices/myhttpcellexample/") {
			httpSliceDir = "cells/myhttpcell/slices/myhttpcellexample"
		}
		if strings.HasPrefix(rel, "cells/myhttpcell/slices/myhttpcelleventexample/") {
			eventSliceDir = "cells/myhttpcell/slices/myhttpcelleventexample"
		}
	}
	if httpSliceDir == "" {
		t.Errorf("HTTP slice dir cells/myhttpcell/slices/myhttpcellexample not found in plan")
	}
	if eventSliceDir == "" {
		t.Errorf("event slice dir cells/myhttpcell/slices/myhttpcelleventexample not found in plan")
	}
}

// TestPlanCellBundleScaffold_MergedPlan verifies that PlanCellBundleScaffold
// returns a merged plan containing both skeleton files (ForceOverwrite=false)
// and derived codegen files (ForceOverwrite=true), with no duplicate AbsPath.
// Nothing is written to the project tree.
//
// GREEN: PlanCellBundleScaffold delegates to appendDerivedCodegenStaged which
// merges skeleton + derived into a single plan in-memory.
func TestPlanCellBundleScaffold_MergedPlan(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Must have go.mod for metadata.NewParser to work.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Must have the shared error schema for contractgen to work.
	schemaDir := filepath.Join(dir, "contracts", "shared", "errors")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Read from real repo and write to staging root.
	realSchemaPath := filepath.Join("..", "..", "..", "contracts", "shared", "errors", "error-response-v1.schema.json")
	schemaContent, err := os.ReadFile(realSchemaPath) //nolint:gosec // test fixture from known path
	if err != nil {
		t.Skipf("cannot read shared error schema (not in expected location): %v", err)
	}
	schemaOut := filepath.Join(schemaDir, "error-response-v1.schema.json")
	//nolint:gosec // tempdir test fixture
	if err := os.WriteFile(schemaOut, schemaContent, 0o644); err != nil {
		t.Fatal(err)
	}

	realRoot, err := pathsafe.ResolveRoot(dir)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}

	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("plantestcell"),
		StructName:       "PlanTestCell",
		Package:          "plantestcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	plan, err := PlanCellBundleScaffold(realRoot, spec)
	if err != nil {
		t.Fatalf("PlanCellBundleScaffold: %v", err)
	}

	// No duplicate AbsPath.
	seen := make(map[string]int)
	for _, f := range plan {
		seen[f.AbsPath]++
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("duplicate AbsPath in merged plan: %s (count=%d)", path, count)
		}
	}

	// Skeleton files must exist with ForceOverwrite=false.
	skeletonRels := []string{
		"cells/plantestcell/cell.yaml",
		"cells/plantestcell/cell.go",
	}
	for _, rel := range skeletonRels {
		abs := filepath.Join(realRoot, filepath.FromSlash(rel))
		found := false
		for _, f := range plan {
			if f.AbsPath == abs {
				if f.IsForceOverwrite() {
					t.Errorf("skeleton file %s must have ForceOverwrite=false", rel)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("skeleton file missing from merged plan: %s", rel)
		}
	}

	// Derived files must exist with ForceOverwrite=true.
	derivedRels := []string{
		"cells/plantestcell/cell_gen.go",
		"generated/contracts/http/plantestcell/example/v1/types_gen.go",
	}
	for _, rel := range derivedRels {
		abs := filepath.Join(realRoot, filepath.FromSlash(rel))
		found := false
		for _, f := range plan {
			if f.AbsPath == abs {
				if !f.IsForceOverwrite() {
					t.Errorf("derived file %s must have ForceOverwrite=true", rel)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("derived file missing from merged plan: %s", rel)
		}
	}

	// Nothing written to project tree (plan is in-memory only).
	if _, statErr := os.Stat(filepath.Join(realRoot, "cells", "plantestcell")); statErr == nil {
		t.Error("PlanCellBundleScaffold must not write to project tree")
	}
}

// ---------------------------------------------------------------------------
// F2 / F10: temp-dir cleanup + dup-guard backstop + cross-stage rollback
// ---------------------------------------------------------------------------

// TestMaterializeSkeletonStage_NoLeakOnInnerFailure asserts that when
// materializeSkeletonStage fails after creating the temp dir (e.g. the
// WritePlannedFiles step returns an error), no gocell-scaffold-stage-* entry
// remains in os.TempDir(). This locks the F2 fix: named return + deferred
// cleanup inside materializeSkeletonStage.
func TestMaterializeSkeletonStage_NoLeakOnInnerFailure(t *testing.T) {
	// Not parallel: mutates package-level stageTempParent via
	// isolateStageParent. Confining it to the sequential test phase means no
	// concurrent materializeSkeletonStage reader can race the var, and the
	// leak scan is over a private dir so a sibling test's staging dir can
	// never be misattributed as a leak.
	if runtime.GOOS == "windows" {
		t.Skip("temp-dir listing semantics differ on windows")
	}

	stageParent := isolateStageParent(t)
	// Private parent starts empty; snapshot for symmetry/robustness.
	stageBefore := listStageDirs(t, stageParent)

	// Feed a skeleton plan that rebases from a realRoot that does NOT match
	// the AbsPath prefix — filepath.Rel will produce a relative path with ".."
	// segments if we pass a totally different root, but actually to trigger the
	// WritePlannedFiles failure we can use a containment-escape path.
	// Simplest reliable failure: pass an AbsPath whose Rel(...) call succeeds
	// but then pathsafe.WritePlannedFiles fails because the plan contains an
	// absolute path outside stageRoot (symlink-escape).
	//
	// Even simpler: use a plan with a file whose content triggers an OS-level
	// write error by making the stageRoot read-only after MkdirTemp — but that
	// is fragile as root. Instead, just make realRoot → AbsPath relationship
	// inconsistent so filepath.Rel returns a "../../..." path, causing
	// pathsafe.WritePlannedFiles to reject it (containment check).
	realRoot := t.TempDir()
	resolved, err := pathsafe.ResolveRoot(realRoot)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	// Craft a plan entry whose AbsPath is NOT under resolved — triggers
	// pathsafe containment rejection inside materializeSkeletonStage.
	outsideDir := t.TempDir()
	badPlan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(outsideDir, "evil.txt"), Content: []byte("bad")},
	}

	_, stageErr := materializeSkeletonStage(resolved, badPlan)
	if stageErr == nil {
		t.Fatal("materializeSkeletonStage with outside AbsPath: want error, got nil")
	}

	// No new stage dirs should remain after the failure.
	stageAfter := listStageDirs(t, stageParent)
	leaked := setDiff(stageAfter, stageBefore)
	if len(leaked) > 0 {
		t.Errorf("temp-dir leak: %d gocell-scaffold-stage-* dir(s) remain after inner failure: %v",
			len(leaked), leaked)
	}
}

// listStageDirs returns the set of gocell-scaffold-stage-* entries under
// parent (the isolated per-test staging parent set by isolateStageParent).
func listStageDirs(t *testing.T, parent string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", parent, err)
	}
	out := make(map[string]struct{})
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "gocell-scaffold-stage-") {
			out[filepath.Join(parent, e.Name())] = struct{}{}
		}
	}
	return out
}

// isolateStageParent points materializeSkeletonStage at a private per-test
// temp parent (via package-level stageTempParent) so leak assertions scan
// only this test's staging dirs and cannot misattribute a sibling parallel
// test's gocell-scaffold-stage-* dir as a leak. Callers must NOT be
// t.Parallel(): the var mutation is confined to the sequential test phase so
// no concurrent materializeSkeletonStage reader races it. Restored on cleanup.
func isolateStageParent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := stageTempParent
	stageTempParent = dir
	t.Cleanup(func() { stageTempParent = prev })
	return dir
}

// setDiff returns elements in a that are not in b.
func setDiff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// TestPlanCellBundle_WithBothFlags_DupGuardIsBackstop asserts that the OLD
// behavior (event reuses HTTP sliceID when withHTTP=true) would produce
// duplicate AbsPath entries that pathsafe's duplicate-detection pass rejects.
// This proves the dup-guard is the actual backstop and that planCellBundle's
// distinct-sliceID fix closed the primary window.
//
// The test reconstructs the old behavior by calling planEventExampleArtifacts
// with withHTTP=false (forcing same sliceID as HTTP) and then confirming that
// when merged with HTTP artifacts, duplicates appear.
func TestPlanCellBundle_WithBothFlags_DupGuardIsBackstop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	realRoot, err := pathsafe.ResolveRoot(dir)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}

	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("dupguardcell"),
		StructName:       "DupGuardCell",
		Package:          "dupguardcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
	}
	cellNoDash := strings.ReplaceAll(spec.CellID.String(), "-", "")
	sliceID := cellNoDash + "example"

	// HTTP artifacts.
	httpItems, err := planHTTPExampleArtifacts(realRoot, spec, cellNoDash, sliceID)
	if err != nil {
		t.Fatalf("planHTTPExampleArtifacts: %v", err)
	}

	// OLD behavior: event artifacts with same sliceID (withHTTP=false in old code).
	// We simulate by calling planEventExampleArtifacts with withHTTP=false,
	// which keeps the same sliceID instead of appending "eventexample".
	oldEventItems, err := planEventExampleArtifacts(realRoot, spec, cellNoDash, sliceID, false /* withHTTP=false → same sliceID */)
	if err != nil {
		t.Fatalf("planEventExampleArtifacts (old behavior): %v", err)
	}

	combined := make([]pathsafe.PlannedFile, 0, len(httpItems)+len(oldEventItems))
	combined = append(combined, httpItems...)
	combined = append(combined, oldEventItems...)

	// Detect duplicates — the dup-guard backstop must fire.
	seen := make(map[string]int)
	for _, f := range combined {
		seen[f.AbsPath]++
	}
	var dups []string
	for path, count := range seen {
		if count > 1 {
			rel, _ := filepath.Rel(realRoot, path)
			dups = append(dups, rel)
		}
	}
	if len(dups) == 0 {
		t.Fatal("expected duplicate AbsPath entries with old same-sliceID behavior; got none — dup-guard backstop not exercised")
	}
	// Verify NewPlanSet rejects the duplicate plan (dup-guard now lifted to
	// type-system Hard at PlanSet construction time —
	// PATHSAFE-PLANSET-TYPED-HARD-01).
	if _, err := pathsafe.NewPlanSet(combined); err == nil {
		t.Error("NewPlanSet must reject duplicate AbsPath plan; got nil error")
	}
}

// TestPlanCellBundleScaffold_DirAtDerivedPath_PlanTimeReject asserts that a
// pre-existing directory at a derived ForceOverwrite path is rejected at PLAN
// time (planDerivedArtifact's governance-gate read fails fast: a directory is
// neither absent nor a generated file), not deferred to writePass. This is
// the cellgen-side half of F2's dry-run/live parity: the conflict surfaces
// from PlanCellBundleScaffold itself, before any plan executes, so dry-run
// and live agree. Nothing is written to the project tree and no
// gocell-scaffold-stage-* dir leaks (F3).
func TestPlanCellBundleScaffold_DirAtDerivedPath_PlanTimeReject(t *testing.T) {
	// Not parallel: mutates package-level stageTempParent (see
	// isolateStageParent godoc).
	if runtime.GOOS == "windows" {
		t.Skip("MkdirAll on conflict path semantics differ on windows")
	}

	stageParent := isolateStageParent(t)
	root := t.TempDir()
	// Must have go.mod for metadata.NewParser to work inside appendDerivedCodegen.
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Include shared error schema so contractgen can render.
	schemaDir := filepath.Join(root, "contracts", "shared", "errors")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realSchemaPath := filepath.Join("..", "..", "..", "contracts", "shared", "errors", "error-response-v1.schema.json")
	schemaContent, schemaErr := os.ReadFile(realSchemaPath) //nolint:gosec // test fixture
	if schemaErr != nil {
		t.Skipf("cannot read shared error schema: %v", schemaErr)
	}
	schemaOut := filepath.Join(schemaDir, "error-response-v1.schema.json")
	if err := os.WriteFile(schemaOut, schemaContent, 0o644); err != nil { //nolint:gosec // tempdir test fixture
		t.Fatal(err)
	}

	// Pre-place a directory at the derived types_gen.go path. planDerivedArtifact
	// must reject it at plan-construction time (read → "is a directory").
	conflictDir := filepath.Join(root, "generated", "contracts", "http", "rollbackstage", "example", "v1", "types_gen.go")
	if err := os.MkdirAll(conflictDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stageBefore := listStageDirs(t, stageParent)

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}

	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("rollbackstage"),
		StructName:       "RollbackStage",
		Package:          "rollbackstage",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	_, planErr := PlanCellBundleScaffold(realRoot, spec)
	if planErr == nil {
		t.Fatal("PlanCellBundleScaffold with a directory at the derived path: want plan-time error, got nil")
	}

	// PlanCellBundleScaffold only stages + plans; it must never write the
	// skeleton onto the project tree (the plan is never executed here).
	for _, rel := range []string{
		"cells/rollbackstage/cell.yaml",
		"cells/rollbackstage/cell.go",
		"cells/rollbackstage/slices/rollbackstageexample/slice.yaml",
	} {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("plan-time reject must not pollute project tree: %s", rel)
		}
	}

	// No staging temp dir must remain.
	stageAfter := listStageDirs(t, stageParent)
	leaked := setDiff(stageAfter, stageBefore)
	if len(leaked) > 0 {
		t.Errorf("staging temp dir leaked after write failure: %v", leaked)
	}
}

// TestPlanCellBundleScaffold_RefusesNonGeneratedDerivedFile is the F1
// regression lock: a hand-written (non gocell-generated) file pre-placed at a
// derived-artifact path must NOT be silently overwritten. PR #544 routed
// derived writes through pathsafe with unconditional ForceOverwrite=true,
// dropping the governance.IsGoCellGenerated gate the legacy codegen.Write
// enforced. planDerivedArtifact restores it: PlanCellBundleScaffold must
// return an error and the pre-existing file content must be untouched.
func TestPlanCellBundleScaffold_RefusesNonGeneratedDerivedFile(t *testing.T) {
	// Not parallel: isolateStageParent mutates package-level stageTempParent.
	if runtime.GOOS == "windows" {
		t.Skip("temp-dir listing semantics differ on windows")
	}
	_ = isolateStageParent(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	schemaDir := filepath.Join(root, "contracts", "shared", "errors")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realSchemaPath := filepath.Join("..", "..", "..", "contracts", "shared", "errors", "error-response-v1.schema.json")
	schemaContent, schemaErr := os.ReadFile(realSchemaPath) //nolint:gosec // test fixture
	if schemaErr != nil {
		t.Skipf("cannot read shared error schema: %v", schemaErr)
	}
	schemaOut := filepath.Join(schemaDir, "error-response-v1.schema.json")
	if err := os.WriteFile(schemaOut, schemaContent, 0o644); err != nil { //nolint:gosec // tempdir test fixture
		t.Fatal(err)
	}

	// Pre-place a HAND-WRITTEN (no gocell header) regular file at a derived
	// contract artifact path.
	derivedRel := filepath.Join("generated", "contracts", "http", "guardstage", "example", "v1", "types_gen.go")
	derivedAbs := filepath.Join(root, derivedRel)
	if err := os.MkdirAll(filepath.Dir(derivedAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	handwritten := []byte("package handwritten\n\n// human-authored, NOT generated\n")
	if err := os.WriteFile(derivedAbs, handwritten, 0o644); err != nil {
		t.Fatal(err)
	}

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	spec := ScaffoldSpec{
		CellID:           scaffoldid.MustParse("guardstage"),
		StructName:       "GuardStage",
		Package:          "guardstage",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	_, planErr := PlanCellBundleScaffold(realRoot, spec)
	if planErr == nil {
		t.Fatal("PlanCellBundleScaffold over a non-generated derived file: want error, got nil")
	}
	if !strings.Contains(planErr.Error(), "non-generated") {
		t.Errorf("error must name the non-generated overwrite refusal; got: %v", planErr)
	}

	// The hand-written file must be byte-identical (never overwritten).
	got, readErr := os.ReadFile(derivedAbs) //nolint:gosec // tempdir test fixture, path constructed in-test
	if readErr != nil {
		t.Fatalf("read pre-placed file: %v", readErr)
	}
	if !bytes.Equal(got, handwritten) {
		t.Errorf("hand-written file was modified: got %q want %q", got, handwritten)
	}
}

// TestScaffoldSpec_DefaultsToL2 asserts that an empty ConsistencyLevel is
// defaulted to "L2" before validation, and that a bundle scaffold succeeds.
func TestScaffoldSpec_DefaultsToL2(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     scaffoldid.MustParse("defaultlvlcell"),
		StructName: "DefaultLvlCell",
		Package:    "defaultlvlcell",
		ModulePath: "github.com/ghbvf/gocell",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
		Type:       "core",
		// ConsistencyLevel intentionally empty — should default to L2
		WithHTTP: true,
	}
	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("default ConsistencyLevel scaffold failed: %v", err)
	}
	// Verify the generated cell.yaml contains consistencyLevel: L2
	cellYAMLPath := dir + "/cells/defaultlvlcell/cell.yaml"
	data, err := os.ReadFile(cellYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read cell.yaml: %v", err)
	}
	if !strings.Contains(string(data), "consistencyLevel: L2") {
		t.Errorf("expected cell.yaml to contain 'consistencyLevel: L2', got:\n%s", string(data))
	}
}
